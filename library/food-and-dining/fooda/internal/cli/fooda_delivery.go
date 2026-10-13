package cli

// pp:data-source auto

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/printing-press-library/library/food-and-dining/fooda/internal/client"
	"github.com/mvanhorn/printing-press-library/library/food-and-dining/fooda/internal/store"
)

const (
	deliveryDelivered = "delivered"
	deliveryScheduled = "scheduled"
	deliveryUnknown   = "unknown"
)

// weekAheadDroppedNote is shown when week-ahead has no event for today: Fooda
// removes an event from the live feed once it is delivered, so an empty today
// is not evidence that nothing was ordered.
const weekAheadDroppedNote = "Note: no event is listed for today. Delivered events drop off this list; " +
	"run 'fooda-pp-cli delivery-status' to check today's order."

// Fooda reports every placed order as status "checkout_complete", delivered or
// not, so the human-readable delivery text is the only signal that separates a
// delivered order from a scheduled one.
var deliveryTextRe = regexp.MustCompile(`(?i)^\s*(delivered|delivering) on (\d{1,2})/(\d{1,2})/(\d{4})\b`)

// classifyDelivery maps an order's delivery text to a state and the calendar
// date (YYYY-MM-DD) it names. Text it does not recognize, or a date that does
// not exist, is deliveryUnknown with an empty date so callers can fall back
// rather than guess.
func classifyDelivery(text string) (state, date string) {
	m := deliveryTextRe.FindStringSubmatch(text)
	if m == nil {
		return deliveryUnknown, ""
	}
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	year, _ := strconv.Atoi(m[4])
	d := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if int(d.Month()) != month || d.Day() != day {
		return deliveryUnknown, ""
	}
	state = deliveryDelivered
	if strings.EqualFold(m[1], "delivering") {
		state = deliveryScheduled
	}
	return state, d.Format("2006-01-02")
}

// resolveDeliveryDate turns the --date flag into YYYY-MM-DD. "today" (or empty)
// is the caller's local calendar day.
func resolveDeliveryDate(flag string, now time.Time) (string, error) {
	v := strings.ToLower(strings.TrimSpace(flag))
	if v == "" || v == "today" {
		return now.Format("2006-01-02"), nil
	}
	d, err := time.Parse("2006-01-02", v)
	if err != nil {
		return "", fmt.Errorf("invalid --date %q: use 'today' or YYYY-MM-DD", flag)
	}
	return d.Format("2006-01-02"), nil
}

type deliveryRow struct {
	Date     string `json:"date"`
	State    string `json:"state"`
	Delivery string `json:"delivery"`
	Vendor   string `json:"vendor"`
	Items    string `json:"items"`
	UUID     string `json:"uuid"`
}

// deliveryRowsForDate returns the orders that fall on target (YYYY-MM-DD), in
// input order. An order whose delivery text has no usable date is matched on
// its fulfilled-time date instead of being dropped, and keeps its raw text.
func deliveryRowsForDate(orders []client.PastOrdersProps, target string) []deliveryRow {
	rows := []deliveryRow{}
	for _, props := range orders {
		for _, pres := range props.Presenter {
			o := pres.Order
			state, date := classifyDelivery(o.Delivery)
			if date == "" {
				date = time.Unix(o.OrderFulfilledTimeUnix, 0).Format("2006-01-02")
			}
			if date != target {
				continue
			}
			rows = append(rows, deliveryRow{
				Date:     date,
				State:    state,
				Delivery: o.Delivery,
				Vendor:   strings.Join(o.VendorNames, ", "),
				Items:    strings.Join(o.ItemNames, "; "),
				UUID:     o.RequestID,
			})
		}
	}
	return rows
}

// loadSyncedOrders reads order history from the synced store. A store that
// exists but holds no orders is a valid empty history (a sync can legitimately
// find none); only a missing store is an error. syncedAt is zero when no sync
// time was recorded.
func loadSyncedOrders(ctx context.Context) (orders []client.PastOrdersProps, syncedAt time.Time, err error) {
	db, err := store.OpenReadOnlyContext(ctx, defaultDBPath("fooda-pp-cli"))
	if err != nil || db == nil {
		return nil, time.Time{}, notFoundErr(fmt.Errorf("local database not found; run 'sync' first to populate local orders"))
	}
	defer db.Close()
	orders, err = loadAndDeduplicateOrders(db)
	if err != nil {
		return nil, time.Time{}, err
	}
	if _, at, _, stateErr := db.GetSyncState("order"); stateErr == nil {
		syncedAt = at
	}
	return orders, syncedAt, nil
}

func loadLiveOrders(ctx context.Context, flags *rootFlags) ([]client.PastOrdersProps, error) {
	c, err := flags.newClient()
	if err != nil {
		return nil, err
	}
	raw, err := c.GetHTML(ctx, "/settings/orders")
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	props, err := client.ParsePastOrders(string(raw))
	if err != nil {
		return nil, err
	}
	return []client.PastOrdersProps{*props}, nil
}

// loadPastOrders reads order history from the live orders page, or from the
// synced store with --data-source local. With --data-source auto a network
// failure falls back to the synced store, warns on warn, and marks the result
// local; other failures (auth, HTTP errors) are not papered over with old data.
// The client is only built for the live path so local reads work without a
// session.
func loadPastOrders(ctx context.Context, flags *rootFlags, warn io.Writer) ([]client.PastOrdersProps, error) {
	if flags.dataSource == "local" {
		orders, _, err := loadSyncedOrders(ctx)
		return orders, err
	}

	orders, err := loadLiveOrders(ctx, flags)
	if err == nil || flags.dataSource == "live" || !isNetworkError(err) {
		return orders, err
	}

	synced, syncedAt, localErr := loadSyncedOrders(ctx)
	if localErr != nil {
		return nil, fmt.Errorf("live fetch failed: %w (no local database fallback available)", err)
	}
	// Delivery state is a snapshot: an order synced as scheduled may be
	// delivered by now, so say how old the data is.
	msg := fmt.Sprintf("warning: live fetch failed (%v); falling back to local database", err)
	if !syncedAt.IsZero() {
		msg += fmt.Sprintf("; orders last synced %s, so delivery state may be out of date", syncedAt.Local().Format("2006-01-02 15:04"))
	}
	fmt.Fprintln(warn, msg)
	flags.agentSource = "local"
	return synced, nil
}

func init() {
	registerNovelCommand(func(rootCmd *cobra.Command, flags *rootFlags) {
		var date string
		cmd := &cobra.Command{
			Use:   "delivery-status",
			Short: "Check whether your order for a day (default today) was delivered",
			Long: strings.Trim(`
Check whether your order for a day was delivered, from your order history.

Fooda drops a day's event from "events" and "week-ahead" once it is delivered, and
the My Fooda page then jumps ahead with no status, so a missing event does not mean
there was no order. This reads the most recent page of order history instead and
reports each order on the date as delivered, scheduled, or unknown (delivery text it
does not recognize is kept verbatim). --date is matched against your local calendar.

If the network is unreachable, --data-source auto falls back to your last sync (run
'sync' to populate it); synced delivery state can be out of date.
`, "\n"),
			Example: strings.Trim(`
  fooda-pp-cli delivery-status
  fooda-pp-cli delivery-status --date 2026-03-04 --agent
`, "\n"),
			Annotations: map[string]string{"pp:data-source": "auto", "mcp:read-only": "true"},
			RunE: func(cmd *cobra.Command, args []string) error {
				if dryRunOK(flags) {
					return writeDryRun(cmd.OutOrStdout(), flags, "delivery-status")
				}

				target, err := resolveDeliveryDate(date, time.Now())
				if err != nil {
					return usageErr(err)
				}

				ctx, cancel := boundCtx(cmd.Context(), flags)
				defer cancel()

				orders, err := loadPastOrders(ctx, flags, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				rows := deliveryRowsForDate(orders, target)

				if flags.asJSON {
					return printJSONFiltered(cmd.OutOrStdout(), rows, flags)
				}
				if len(rows) == 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "No order found for %s.\n", target)
					return nil
				}
				w := newTabWriter(cmd.OutOrStdout())
				fmt.Fprintln(w, "Date\tState\tDelivery\tVendor\tItems")
				for _, r := range rows {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Date, r.State, r.Delivery, r.Vendor, r.Items)
				}
				return w.Flush()
			},
		}
		cmd.Flags().StringVar(&date, "date", "today", "Day to check: 'today' or YYYY-MM-DD")
		rootCmd.AddCommand(cmd)
	})
}
