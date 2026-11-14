package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sdrahn/custos/internal/category"
	"github.com/sdrahn/custos/internal/host"
	"github.com/sdrahn/custos/internal/journal"
)

// Task categories and the promotion path (CONTRACT.md §6a, O11).

// Categories reads the catalog.
func (a *Admin) Categories() (map[string]category.Category, error) { return category.Load(a.Root) }

func (a *Admin) checkCategory(cat string) error { return category.Check(a.Root, cat) }

// Promotion is promote's input.
type Promotion struct {
	Envelope []byte
	// Reason is the person's; required with DespiteReadiness.
	Reason string
	// DespiteReadiness promotes although readiness is not met (§6a).
	DespiteReadiness bool
}

// Promote moves a category of an agent to bounded within an envelope.
func (a *Admin) Promote(name, cat, by string, p Promotion) error {
	if err := host.CheckName(name); err != nil {
		return err
	}
	if err := a.checkCategory(cat); err != nil {
		return err
	}
	if cat == Diagnostics {
		return errors.New("category diagnostics only reads: its calls need no approval, so it is never promoted")
	}
	if err := a.checkCategoryEnabled(name, cat); err != nil {
		return err
	}
	env, err := category.ParseEnvelope(p.Envelope)
	if err != nil {
		return err
	}
	if p.DespiteReadiness && strings.TrimSpace(p.Reason) == "" {
		return errors.New("--despite-readiness needs --reason TEXT")
	}
	d, err := a.readGateway(name)
	if err != nil {
		return err
	}
	if d.Mode == "advisory" {
		return fmt.Errorf("agent %s is advisory, which caps every category: set its mode to supervised first", name)
	}
	if !hasCategory(d, cat) {
		return fmt.Errorf("agent %s has no permission in category %s: give it one (permissions, category: %s) first", name, cat, cat)
	}
	from := "supervised"
	if c := d.Categories[cat]; c != nil && c.Mode != "" {
		from = c.Mode
	}
	if from == "bounded" && a.sameEnvelope(d.Categories[cat].Envelope, env) {
		return fmt.Errorf("agent %s: category %s is already bounded with this envelope", name, cat)
	}
	rep, err := a.Readiness(name, cat)
	if err != nil {
		return err
	}
	ready := rep.Categories[cat]
	if !ready.Ready && !p.DespiteReadiness {
		return fmt.Errorf("agent %s: category %s is not ready (%s); see agent-admin readiness %s %s, or promote with --despite-readiness --reason TEXT",
			name, cat, strings.Join(ready.Unmet, "; "), name, cat)
	}
	readinessJSON, _ := json.Marshal(ready)
	id := "p-" + a.Secret()
	if len(id) > 18 {
		id = id[:18]
	}
	// To the nanosecond: the review and the evaluator count the records
	// after it (journal timestamps are microseconds), and a decline in the
	// same second just before the promotion must not count against it.
	now := a.Now().UTC().Format(time.RFC3339Nano)
	if d.Categories == nil {
		d.Categories = map[string]*category.State{}
	}
	d.Categories[cat] = &category.State{Mode: "bounded", Envelope: env, PromotedBy: by + " " + now, Promotion: id}
	if err := a.writeGateway(name, d, false); err != nil {
		return err
	}
	a.Record(journal.CategoryPromoted, name, fmt.Sprintf("%s promoted %s of agent %s to bounded", by, cat, name), journal.Fields{
		"CATEGORY": cat, "PROMOTION": id, "MODE_FROM": from, "MODE_TO": "bounded", "BY": by,
		"REASON": p.Reason, "ENVELOPE_SHA256": category.EnvelopeHash(env), "READINESS": string(readinessJSON),
	})
	// The demotion evaluator must run while any category is bounded.
	if err := a.Run(nil, "systemctl", "enable", "--now", "custos-demote.timer"); err != nil {
		fmt.Fprintf(a.Out, "agent-admin: custos-demote.timer could not be enabled (%v): nothing demotes %s of %s automatically until it runs\n", err, cat, name)
	}
	fmt.Fprintf(a.Out, "agent %s: category %s is bounded within its envelope (promotion %s)\n", name, cat, id)
	return nil
}

// Demote moves a category back to supervised; its envelope stays for a
// later promotion.
func (a *Admin) Demote(name, cat, by, reason string) error {
	if err := host.CheckName(name); err != nil {
		return err
	}
	d, err := a.readGateway(name)
	if err != nil {
		return err
	}
	c := d.Categories[cat]
	if c == nil || c.Mode != "bounded" {
		return fmt.Errorf("agent %s: category %s is not promoted", name, cat)
	}
	c.Mode = "supervised"
	c.DemotedBy = by + " " + a.Now().UTC().Format(time.RFC3339Nano)
	if err := a.writeGateway(name, d, true); err != nil {
		return err
	}
	a.Record(journal.CategoryDemoted, name, fmt.Sprintf("%s demoted %s of agent %s to supervised", by, cat, name), journal.Fields{
		"CATEGORY": cat, "PROMOTION": c.Promotion, "MODE_FROM": "bounded", "MODE_TO": "supervised", "BY": by,
		"REASON": reason, "ENVELOPE_SHA256": category.EnvelopeHash(c.Envelope),
	})
	fmt.Fprintf(a.Out, "agent %s: category %s is supervised\n", name, cat)
	return nil
}

func hasCategory(d *GatewayData, cat string) bool {
	for _, p := range d.Permissions {
		if p["category"] == cat {
			return true
		}
	}
	return false
}

func (a *Admin) sameEnvelope(x, y *category.Envelope) bool {
	return category.EnvelopeHash(x) == category.EnvelopeHash(y)
}
