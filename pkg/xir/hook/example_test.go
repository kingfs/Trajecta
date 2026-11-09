package hook_test

import (
	"context"
	"fmt"

	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// exampleTagHook tags every request message it sees.
type exampleTagHook struct {
	id string
}

func (h exampleTagHook) ID() string { return h.id }

func (h exampleTagHook) Points() []hook.Point { return []hook.Point{hook.PointRequestMessage} }

func (h exampleTagHook) Lanes() hook.Lane { return hook.LaneHot }

func (h exampleTagHook) Needs() hook.Needs { return hook.NeedsSemantic }

func (h exampleTagHook) Priority() int { return 10 }

func (h exampleTagHook) FailPolicy() hook.FailPolicy { return hook.FailOpen }

func (h exampleTagHook) Handle(_ context.Context, ev *hook.Event) (hook.Result, error) {
	return hook.Result{
		Findings: []hook.Finding{{
			Hook:     h.id,
			Rule:     "example.tag",
			Severity: hook.SeverityInfo,
			Point:    ev.Point,
			Message:  "message seen",
		}},
		Tags: map[string]string{"seen": "yes"},
	}, nil
}

// ExamplePlan_Run registers one hook, compiles the plan and runs it for a
// single event.
func ExamplePlan_Run() {
	reg := &hook.Registry{}
	reg.Register("tag", func(cfg hook.Config) (hook.Hook, error) {
		return exampleTagHook{id: cfg.ID}, nil
	})

	plan, err := hook.Compile(reg, []hook.Config{{ID: "tag", Enabled: true}})
	if err != nil {
		fmt.Println("compile:", err)
		return
	}
	res, err := plan.Run(context.Background(), &hook.Event{
		Lane:  hook.LaneHot,
		Point: hook.PointRequestMessage,
	})
	if err != nil {
		fmt.Println("run:", err)
		return
	}
	fmt.Println(res.Findings[0].Rule, res.Tags["seen"])

	// Output: example.tag yes
}
