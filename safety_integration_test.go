//go:build integration

// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReferenceCooperativeCancellation(t *testing.T) {
	var manifest struct{ Fixtures []struct{ ID string } }
	readJSON(t, "testdata/fixtures.json", &manifest)
	dir := os.Getenv("ORF_FIXTURE_DIR")
	if dir == "" {
		dir = ".cache/fixtures"
	}
	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.ID, func(t *testing.T) {
			input, err := os.Open(filepath.Join(dir, fixture.ID+".orf"))
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			info, err := input.Stat()
			if err != nil {
				t.Fatal(err)
			}
			budget := testBudget(t, 256<<20)
			cancelDuring(t, 20, func(ctx context.Context) error {
				file, err := Open(ctx, input, info.Size(), OpenOptions{Budget: budget})
				if file != nil {
					file.Close()
					return errors.New("partial File returned")
				}
				return err
			}, func() bool { return budgetUsed(budget) > 0 })
			if budgetUsed(budget) != 0 {
				t.Fatal("canceled reference parse leaked")
			}
			file, err := Open(context.Background(), input, info.Size(), OpenOptions{Budget: budget})
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			base := budgetUsed(budget)
			cancelDuring(t, 50, func(ctx context.Context) error {
				raw, err := file.DecodeRaw(ctx)
				if raw != nil {
					raw.Close()
					return errors.New("partial RAW returned")
				}
				return err
			}, func() bool { return budgetUsed(budget) > base })
			if budgetUsed(budget) != base {
				t.Fatal("canceled reference decode leaked")
			}
			raw, err := file.DecodeRaw(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			base = budgetUsed(budget)
			cancelDuring(t, 50, func(ctx context.Context) error {
				out, err := Render(ctx, raw, RenderOptions{AllowEstimatedWhite: true})
				if out != nil {
					out.Close()
					return errors.New("partial rendered image returned")
				}
				return err
			}, func() bool { return budgetUsed(budget) > base })
			if budgetUsed(budget) != base {
				t.Fatal("canceled reference render leaked")
			}
		})
	}
}
