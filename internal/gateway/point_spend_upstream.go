package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func pointCharge(change float64, kind, note string) bool {
	if change >= 0 {
		return false
	}
	s := strings.ToLower(kind + " " + note)
	for _, word := range []string{"expire", "过期", "到期", "refund", "退款", "退回"} {
		if strings.Contains(s, word) {
			return false
		}
	}
	return true
}

func (a *App) accountPointSpending(ctx context.Context, acc M, force bool) M {
	uid, since := text(acc["uid"]), num(acc["added_at"])
	return a.points.refresh(ctx, uid, since, force, func(ctx context.Context, from float64) ([]pointDebit, error) {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		debits := []pointDebit{}
		seen := map[[32]byte]bool{}
		for page := 1; ; page++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			d, err := a.ledger(ctx, acc, page, 100)
			if err != nil {
				return nil, err
			}
			items, oldest := arr(d["items"]), now()
			if len(items) > 0 {
				raw, _ := json.Marshal(items)
				hash := sha256.Sum256(raw)
				if seen[hash] {
					return nil, errors.New("积分账单返回重复分页，请稍后刷新")
				}
				seen[hash] = true
			}
			for _, value := range items {
				x := obj(value)
				at, change := num(x["at"]), num(x["change"])
				if at <= 0 {
					return nil, errors.New("积分账单缺少有效时间，无法统计消耗")
				}
				oldest = min(oldest, at)
				if at >= from && pointCharge(change, text(x["type"]), text(x["note"])) {
					debits = append(debits, pointDebit{ID: text(x["id"]), At: at, Points: -change, Model: text(x["model"])})
				}
			}
			if oldest < from || float64(page*100) >= num(d["total"]) {
				return debits, nil
			}
			if len(items) == 0 {
				return nil, errors.New("积分账单分页不完整，请稍后刷新")
			}
		}
	})
}
