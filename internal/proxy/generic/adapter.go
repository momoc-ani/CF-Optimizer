package generic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	cfnetwork "github.com/cf-optimizer/cf-optimizer/internal/network"
	"github.com/cf-optimizer/cf-optimizer/internal/proxy"
)

const adapterName = "generic-route"

type routePlanPayload struct {
	Plans []cfnetwork.ChangePlan `json:"plans"`
}

type routeReceiptPayload struct {
	TransactionIDs []string `json:"transaction_ids"`
}

// Adapter 通过路由事务控制器为策略 CIDR 建立物理出口路由。
type Adapter struct {
	controller     *cfnetwork.RouteController
	interfaceName  string
	interfaceIndex int
	gatewayIPv4    string
	gatewayIPv6    string
	metric         int
}

// New 创建 Generic Route 适配器。
func New(controller *cfnetwork.RouteController, path cfnetwork.PhysicalPath, metric int) (*Adapter, error) {
	if controller == nil {
		return nil, errors.New("route controller is required")
	}
	return &Adapter{
		controller: controller, interfaceName: path.Interface, interfaceIndex: path.InterfaceIndex,
		gatewayIPv4: path.GatewayIPv4, gatewayIPv6: path.GatewayIPv6, metric: metric,
	}, nil
}

// Name 返回稳定的适配器标识。
func (a *Adapter) Name() string { return adapterName }

// Capabilities 声明 Generic Route 只支持 IP CIDR。
func (a *Adapter) Capabilities() proxy.Capabilities {
	return proxy.Capabilities{IPv4: true, IPv6: true, Rollback: true}
}

// Detect 在物理接口及至少一个网关可用时报告存在。
func (a *Adapter) Detect(context.Context) (proxy.Detection, error) {
	present := a.interfaceName != "" && (a.gatewayIPv4 != "" || a.gatewayIPv6 != "")
	message := "physical route path is available"
	if !present {
		message = "physical interface or gateway is unavailable"
	}
	return proxy.Detection{Present: present, Message: message}, nil
}

// Plan 为每个受管 CIDR 生成无副作用的路由变更计划。
func (a *Adapter) Plan(ctx context.Context, policy proxy.DirectPolicy) (proxy.Plan, error) {
	payload := routePlanPayload{}
	eligibleRoutes := 0
	for _, prefix := range policy.IPv4CIDRs {
		if a.gatewayIPv4 == "" {
			continue
		}
		eligibleRoutes++
		plan, err := a.controller.Plan(ctx, a.route(prefix, a.gatewayIPv4), false)
		if err != nil {
			return proxy.Plan{}, err
		}
		if plan.Previous != nil && !plan.WillReplace {
			continue
		}
		payload.Plans = append(payload.Plans, plan)
	}
	for _, prefix := range policy.IPv6CIDRs {
		if a.gatewayIPv6 == "" {
			continue
		}
		eligibleRoutes++
		plan, err := a.controller.Plan(ctx, a.route(prefix, a.gatewayIPv6), false)
		if err != nil {
			return proxy.Plan{}, err
		}
		if plan.Previous != nil && !plan.WillReplace {
			continue
		}
		payload.Plans = append(payload.Plans, plan)
	}
	if eligibleRoutes == 0 {
		return proxy.Plan{}, errors.New("policy has no CIDR with an available physical gateway")
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return proxy.Plan{}, err
	}
	return proxy.Plan{
		ID: fmt.Sprintf("generic-%d", time.Now().UnixNano()), Adapter: adapterName, Policy: policy,
		Summary: []string{fmt.Sprintf("replace and verify %d physical routes", len(payload.Plans))}, Payload: rawPayload,
	}, nil
}

// Apply 顺序应用路由计划，任一失败时立即逆序回滚已应用事务。
func (a *Adapter) Apply(ctx context.Context, plan proxy.Plan) (proxy.Receipt, error) {
	if plan.Adapter != adapterName {
		return proxy.Receipt{}, errors.New("plan does not belong to generic route adapter")
	}
	var payload routePlanPayload
	if err := json.Unmarshal(plan.Payload, &payload); err != nil {
		return proxy.Receipt{}, fmt.Errorf("decode generic route plan: %w", err)
	}
	receiptPayload := routeReceiptPayload{}
	for _, routePlan := range payload.Plans {
		transaction, err := a.controller.Apply(ctx, routePlan)
		if err != nil {
			rollbackErr := a.rollbackTransactions(context.WithoutCancel(ctx), receiptPayload.TransactionIDs)
			return proxy.Receipt{}, errors.Join(err, rollbackErr)
		}
		receiptPayload.TransactionIDs = append(receiptPayload.TransactionIDs, transaction.ID)
	}
	rawReceipt, err := json.Marshal(receiptPayload)
	if err != nil {
		return proxy.Receipt{}, err
	}
	return proxy.Receipt{ID: plan.ID, Adapter: adapterName, Changed: len(receiptPayload.TransactionIDs) > 0, AppliedAt: time.Now().UTC(), Payload: rawReceipt}, nil
}

// Verify 确认收据中的每个路由事务都已经通过实际选路验证。
func (a *Adapter) Verify(_ context.Context, _ proxy.DirectPolicy, receipt proxy.Receipt) error {
	var payload routeReceiptPayload
	if err := json.Unmarshal(receipt.Payload, &payload); err != nil {
		return err
	}
	states := map[string]string{}
	for _, transaction := range a.controller.Transactions() {
		states[transaction.ID] = transaction.State
	}
	for _, transactionID := range payload.TransactionIDs {
		if states[transactionID] != "verified" {
			return fmt.Errorf("route transaction %s is not verified", transactionID)
		}
	}
	return nil
}

// Rollback 逆序恢复本次 Generic Route 应用的全部事务。
func (a *Adapter) Rollback(ctx context.Context, receipt proxy.Receipt) error {
	var payload routeReceiptPayload
	if err := json.Unmarshal(receipt.Payload, &payload); err != nil {
		return err
	}
	return a.rollbackTransactions(ctx, payload.TransactionIDs)
}

// rollbackTransactions 逆序恢复已应用的路由事务，供部分失败和正式回滚共用。
func (a *Adapter) rollbackTransactions(ctx context.Context, transactionIDs []string) error {
	var rollbackErrors []error
	for index := len(transactionIDs) - 1; index >= 0; index-- {
		if err := a.controller.Rollback(ctx, transactionIDs[index]); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	return errors.Join(rollbackErrors...)
}

func (a *Adapter) route(prefix, gateway string) cfnetwork.RouteSpec {
	return cfnetwork.RouteSpec{
		Prefix: prefix, Gateway: gateway, Interface: a.interfaceName,
		InterfaceIndex: a.interfaceIndex, Metric: a.metric,
	}
}
