// Command orders keeps the order table honest: pending checkouts that VNPay can no longer
// complete are cancelled, so "Chờ thanh toán" in the UI never lies.
//
//	go run ./cmd/orders -stale            # list pending orders older than 15 minutes
//	go run ./cmd/orders -stale -apply     # cancel them
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	stale := flag.Bool("stale", false, "only look at pending orders older than the payment window")
	apply := flag.Bool("apply", false, "actually cancel the rows (default: dry run)")
	flag.Parse()

	if err := run(*stale, *apply); err != nil {
		fmt.Fprintln(os.Stderr, "orders:", err)
		os.Exit(1)
	}
}

func run(stale, apply bool) error {
	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	where := ""
	if stale {
		// VNPay's payment window is 15 minutes; a pending row older than that can never
		// be paid and only confuses the buyer.
		where = " AND created_at < now() - interval '15 minutes'"
	}

	rows, err := db.QueryContext(ctx,
		`SELECT id, vnpay_txn_ref, amount_vnd, created_at FROM orders
		 WHERE status = 'pending'`+where+` ORDER BY id`)
	if err != nil {
		return err
	}

	type row struct {
		id     int64
		ref    string
		amount int64
		at     time.Time
	}
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.ref, &r.amount, &r.at); err != nil {
			rows.Close()
			return err
		}
		found = append(found, r)
	}
	rows.Close()

	if len(found) == 0 {
		fmt.Println("no pending orders to clean up")
		return nil
	}

	for _, r := range found {
		fmt.Printf("  %-3d %-26s %8d  %s\n", r.id, r.ref, r.amount, r.at.Format("02/01 15:04"))
	}

	if !apply {
		fmt.Printf("%d pending order(s); run with -apply to cancel\n", len(found))
		return nil
	}

	res, err := db.ExecContext(ctx,
		`UPDATE orders SET status = 'cancelled'
		 WHERE status = 'pending'`+where)
	if err != nil {
		return err
	}
	updated, _ := res.RowsAffected()
	fmt.Printf("cancelled %d order(s)\n", updated)
	return nil
}
