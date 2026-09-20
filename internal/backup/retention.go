package backup

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
)

type Retention struct {
	Enabled bool `json:"enabled"`
	Daily   int  `json:"daily"`
	Monthly int  `json:"monthly"`
}

func (p Retention) Validate() error {
	if p.Daily < 0 || p.Daily > 3650 || p.Monthly < 0 || p.Monthly > 120 || (p.Enabled && p.Daily < 1) {
		return errors.New("备份保留需为 1–3650 个每日版本、0–120 个月度版本")
	}
	return nil
}

type RetentionPlan struct {
	Keep   []Snapshot `json:"keep"`
	Remove []Snapshot `json:"remove"`
	Token  string     `json:"token"`
}

func retentionPlan(snapshots []Snapshot, p Retention) (RetentionPlan, error) {
	out := RetentionPlan{Keep: []Snapshot{}, Remove: []Snapshot{}}
	if err := p.Validate(); err != nil {
		return out, err
	}
	if !p.Enabled {
		return out, errors.New("尚未启用历史备份保留策略")
	}
	days, months := map[string]bool{}, map[string]bool{}
	for i, s := range snapshots {
		d, m := s.Time.UTC().Format("2006-01-02"), s.Time.UTC().Format("2006-01")
		keep := i == 0
		if !days[d] && len(days) < p.Daily {
			days[d] = true
			keep = true
		}
		if !months[m] && len(months) < p.Monthly {
			months[m] = true
			keep = true
		}
		if keep {
			out.Keep = append(out.Keep, s)
		} else {
			out.Remove = append(out.Remove, s)
		}
	}
	b, _ := json.Marshal(struct {
		Snapshots []Snapshot
		Policy    Retention
	}{snapshots, p})
	out.Token = fmt.Sprintf("%x", sha256.Sum256(b))
	return out, nil
}
func (c Client) PlanRetention(ctx context.Context, p Retention) (RetentionPlan, error) {
	snapshots, err := c.LibrarySnapshots(ctx)
	if err != nil {
		return RetentionPlan{}, err
	}
	return retentionPlan(snapshots, p)
}
func (c Client) ApplyRetention(ctx context.Context, p Retention, token string) error {
	plan, err := c.PlanRetention(ctx, p)
	if err != nil {
		return err
	}
	if plan.Token != token {
		return errors.New("备份历史或规则已变化，请重新预览")
	}
	if len(plan.Keep) == 0 && len(plan.Remove) == 0 {
		return nil
	}
	if len(plan.Keep) == 0 {
		return errors.New("拒绝删除全部备份")
	}
	// Only explicitly listed, filtered library snapshot IDs are passed to forget.
	for start := 0; start < len(plan.Remove); start += 200 {
		args := []string{"forget"}
		for _, s := range plan.Remove[start:min(start+200, len(plan.Remove))] {
			if !snapshotID.MatchString(s.ID) {
				return errors.New("无效快照")
			}
			args = append(args, s.ID)
		}
		if _, err = c.run(ctx, "", args...); err != nil {
			return err
		}
	}
	// Also prune when forget succeeded in an earlier interrupted attempt.
	if _, err = c.run(ctx, "", "prune", "--max-repack-size", "1G"); err != nil {
		return err
	}
	_, err = c.run(ctx, "", "check")
	return err
}
