package optimizer

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
	"github.com/cf-optimizer/cf-optimizer/internal/store"
)

// policyReceiptJournal 将尚未提交的代理收据原子写入主状态文件。
type policyReceiptJournal struct {
	store *store.Store
	now   func() time.Time
}

// newPolicyReceiptJournal 创建与运行器状态共享事务边界的收据日志。
func newPolicyReceiptJournal(stateStore *store.Store) *policyReceiptJournal {
	return &policyReceiptJournal{store: stateStore, now: time.Now}
}

// Begin 在首次策略写入前建立事务；同一运行内的过渡与最终策略共用收据链。
func (j *policyReceiptJournal) Begin(policy proxy.DirectPolicy) error {
	if j == nil || j.store == nil {
		return errors.New("policy receipt journal store is required")
	}
	policyPayload, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	emptyReceipts, err := json.Marshal(proxy.ApplyResult{})
	if err != nil {
		return err
	}
	return j.store.Update(func(state *store.State) error {
		if state.PendingPolicy == nil {
			state.PendingPolicy = store.NewPolicyTransaction(j.now().UTC(), policyPayload, emptyReceipts)
			return nil
		}
		state.PendingPolicy.Policy = policyPayload
		return nil
	})
}

// Record 在适配器应用返回后立即追加单份回滚收据。
func (j *policyReceiptJournal) Record(receipt proxy.Receipt) error {
	if j == nil || j.store == nil {
		return errors.New("policy receipt journal store is required")
	}
	return j.store.Update(func(state *store.State) error {
		if state.PendingPolicy == nil {
			return errors.New("pending policy transaction is missing")
		}
		applied, err := decodeApplyResult(state.PendingPolicy.Receipts)
		if err != nil {
			return err
		}
		applied.Receipts = append(applied.Receipts, receipt)
		encoded, err := json.Marshal(applied)
		if err != nil {
			return err
		}
		state.PendingPolicy.Receipts = encoded
		return nil
	})
}

// Remove 删除已成功回滚的收据，并在事务为空时移除日志。
func (j *policyReceiptJournal) Remove(removed []proxy.Receipt) error {
	if j == nil || j.store == nil {
		return nil
	}
	removedIDs := make(map[string]struct{}, len(removed))
	for _, receipt := range removed {
		removedIDs[receipt.Adapter+"\x00"+receipt.ID] = struct{}{}
	}
	return j.store.Update(func(state *store.State) error {
		if state.PendingPolicy == nil {
			return nil
		}
		applied, err := decodeApplyResult(state.PendingPolicy.Receipts)
		if err != nil {
			return err
		}
		remaining := applied.Receipts[:0]
		for _, receipt := range applied.Receipts {
			if _, remove := removedIDs[receipt.Adapter+"\x00"+receipt.ID]; !remove {
				remaining = append(remaining, receipt)
			}
		}
		if len(remaining) == 0 {
			state.PendingPolicy = nil
			return nil
		}
		applied.Receipts = remaining
		encoded, err := json.Marshal(applied)
		if err != nil {
			return err
		}
		state.PendingPolicy.Receipts = encoded
		return nil
	})
}

func decodeApplyResult(payload json.RawMessage) (proxy.ApplyResult, error) {
	var applied proxy.ApplyResult
	if len(payload) == 0 {
		return applied, nil
	}
	if err := json.Unmarshal(payload, &applied); err != nil {
		return proxy.ApplyResult{}, err
	}
	return applied, nil
}
