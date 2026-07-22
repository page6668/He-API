// AD-002 —— 模型目录快照的可观测性。
//
// 为什么需要它:2026-07-22 网关升级后,目录静默降级回编译期清单,外部表现与
// 「新版本还没滚上来」**完全一致** —— 两者都返回同一份兜底清单。当时只能靠翻
// Pod 日志才分辨出来,期间白等了近一小时。日志本身是有的(每 5 分钟一条 WARN),
// 缺的是一个能被抓取、能配告警的数值。
//
// 这里用可观测量(callback 在采集时才读快照),而不是在刷新路径上打点:目录只有
// 一个写者、请求路径完全不受影响,且即使刷新协程卡死,采集到的仍是当下真实状态。
//
// 不放进 /health:那个端点有 Story 3.1 BR-1.1 的硬契约 —— 只做进程存活检查,
// 明令不查 DB/Redis,响应体构造时序列化一次且要求字节恒定。
package catalogue

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// RegisterMetrics 注册两个可观测量:
//
//	model_catalogue_stale        1 = 正在服务兜底/上一次好快照,0 = 数据库读取成功
//	model_catalogue_models_count 当前对外可见的模型条数
//
// 二者合看才有意义:count=15 且 stale=1 就是本次事故的指纹(15 是编译期清单的
// 条数,数据库里只有 7 条)。告警建议挂在 stale 上,持续 >10 分钟即应人工介入。
//
// 失败不阻断:注册出错只是没有指标,绝不能让网关起不来。
func (s *Snapshot) RegisterMetrics() {
	if s == nil {
		return
	}
	m := otel.Meter("apps/api-gateway/internal/catalogue")

	stale, err := m.Int64ObservableGauge(
		"model_catalogue_stale",
		metric.WithDescription("1 when the model catalogue is serving the compiled-in fallback or a stale snapshot instead of a fresh database read"),
	)
	if err != nil {
		return
	}
	count, err := m.Int64ObservableGauge(
		"model_catalogue_models_count",
		metric.WithDescription("Number of models currently advertised by /v1/models and /public/models"),
	)
	if err != nil {
		return
	}

	_, _ = m.RegisterCallback(
		func(_ context.Context, o metric.Observer) error {
			var v int64
			if s.Stale() {
				v = 1
			}
			o.ObserveInt64(stale, v)
			o.ObserveInt64(count, int64(len(s.Models())))
			return nil
		},
		stale, count,
	)
}
