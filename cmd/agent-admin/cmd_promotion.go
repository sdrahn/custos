package main

import (
	"fmt"
	"strings"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/admin/promotion"
)

// The promotion path (CONTRACT.md §6a): the catalog, a category's
// readiness, promote and demote, the review of autonomous actions, the
// demotion evaluator, and feedback on diagnosis reports (§4a).
var promotionCommands = []command{
	{"categories", func(a *admin.Admin, args []string) error {
		if _, err := parse(flags("categories"), args, 0); err != nil {
			return err
		}
		return printJSON(a.Categories())
	}},
	{"readiness", func(a *admin.Admin, args []string) error {
		pos, err := parse(flags("readiness"), args, 1)
		if err != nil {
			return err
		}
		return printJSON(promotion.Readiness(a, pos[0], pos[1:]...))
	}},
	{"promote", func(a *admin.Admin, args []string) error {
		fs := flags("promote")
		envelope, reason := fs.String("envelope", "", ""), fs.String("reason", "", "")
		despite := fs.Bool("despite-readiness", false, "")
		pos, err := parse(fs, args, 2)
		if err != nil {
			return err
		}
		if *envelope == "" {
			return fmt.Errorf("promote: --envelope FILE is needed")
		}
		b, err := a.ReadFrom(*envelope)
		if err != nil {
			return err
		}
		return promotion.Promote(a, pos[0], pos[1], by(), promotion.Promotion{Envelope: b, Reason: *reason, DespiteReadiness: *despite})
	}},
	{"demote", func(a *admin.Admin, args []string) error {
		fs := flags("demote")
		reason := fs.String("reason", "", "")
		pos, err := parse(fs, args, 2)
		if err != nil {
			return err
		}
		return promotion.Demote(a, pos[0], pos[1], by(), *reason)
	}},
	// review NAME [CATEGORY]: the queue; review NAME CATEGORY ACTIVITY
	// CALL ok|incident: a verdict.
	{"review", func(a *admin.Admin, args []string) error {
		fs := flags("review")
		reason := fs.String("reason", "", "")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		if len(pos) >= 5 {
			return promotion.Review(a, pos[0], pos[1], pos[2], pos[3], pos[4], by(), *reason)
		}
		return printJSON(promotion.ReviewQueue(a, pos[0], pos[1:]...))
	}},
	{"evaluate", func(a *admin.Admin, args []string) error {
		if _, err := parse(flags("evaluate"), args, 0); err != nil {
			return err
		}
		d, err := promotion.Evaluate(a)
		for _, x := range d {
			fmt.Printf("agent %s: category %s demoted: %s\n", x.Agent, x.Category, x.Reason)
			if x.Notice != "" {
				fmt.Printf("agent %s: category %s: %s\n", x.Agent, x.Category, x.Notice)
			}
		}
		return err
	}},
	{"feedback", func(a *admin.Admin, args []string) error {
		fs := flags("feedback")
		var hypotheses list
		fs.Var(&hypotheses, "hypothesis", "")
		plan, comment := fs.String("plan", "", ""), fs.String("comment", "", "")
		executed := fs.Bool("executed", false, "")
		pos, err := parse(fs, args, 2)
		if err != nil {
			return err
		}
		if len(pos) > 2 {
			return fmt.Errorf("feedback: unknown argument %q", pos[2])
		}
		f := promotion.Feedback{Plan: *plan, Comment: *comment, Executed: *executed}
		for _, h := range hypotheses {
			id, verdict, ok := strings.Cut(h, "=")
			if !ok {
				return fmt.Errorf("--hypothesis %s: want ID=confirmed|rejected|unsure", h)
			}
			if f.Hypotheses == nil {
				f.Hypotheses = map[string]string{}
			}
			f.Hypotheses[id] = verdict
		}
		return promotion.RecordFeedback(a, pos[0], pos[1], by(), f)
	}},
}
