package chaos

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
)

type Manager struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Manager {
	return &Manager{cfg: cfg}
}

// Result 定义混沌判定的结果
type Result struct {
	ShouldInject    bool
	Action          string
	Delay           time.Duration
	StatusCode      int
	Message         string
	RuleDescription string
}

// Evaluate 根据模型名判定是否触发规则
func (m *Manager) Evaluate(modelName string) Result {
	if !m.cfg.Chaos.Enabled {
		return Result{ShouldInject: false}
	}

	for _, rule := range m.cfg.Chaos.Rules {
		// 匹配模型: 支持完全匹配或通配符 "*"
		if rule.Model != "*" && !strings.EqualFold(rule.Model, modelName) {
			continue
		}

		// 判定概率
		if rand.Float64() < rule.Rate {
			// 命中规则
			res := Result{
				ShouldInject:    true,
				Action:          normalizeAction(rule.Action),
				Delay:           rule.Delay,
				StatusCode:      rule.StatusCode,
				Message:         rule.Message,
				RuleDescription: fmt.Sprintf("Rule[Model=%s, Action=%s]", rule.Model, rule.Action),
			}

			// 默认值填充
			if res.Action == "error" && res.StatusCode == 0 {
				res.StatusCode = 500
			}
			if res.Action == "error" && res.Message == "" {
				res.Message = "Chaos Injection Error"
			}
			return res
		}
	}

	return Result{ShouldInject: false}
}

// normalizeAction lowercases the configured action the way the router normalizes its
// selection policies, so `Delay` or `ERROR` inject what the operator asked for instead of
// marking the rule as hit and then doing nothing: the proxy matches the action with an exact
// comparison, and an unrecognized value used to fall through both branches while the request
// was forwarded as if chaos were off. Load rejects values this function does not recognize,
// but a Config built in code still goes through here.
func normalizeAction(action string) string {
	return strings.ToLower(strings.TrimSpace(action))
}
