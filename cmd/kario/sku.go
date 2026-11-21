package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
)

// skusJSON is what the developer machine's Shell can simulate of each SKU
// (24-sku-framework.md), generated from sku/ by tools/sku.py sims;
// tools/sku_test.py fails when it is stale.
//
//go:embed skus.json
var skusJSON []byte

// simSKU is one SKU: the form factor's screen and keypad, and what the
// Shell is given as its sku.json (shell.SKU) under -sku.
type simSKU struct {
	ID       string          `json:"id"`
	Form     string          `json:"form"`
	Width    int             `json:"width"`
	Height   int             `json:"height"`
	Emulator bool            `json:"emulator"`
	Shell    json.RawMessage `json:"shell"`
	info     struct {
		Board    string `json:"board"`
		Device   string `json:"device"`
		Display  string `json:"display"`
		Keypad   string `json:"keypad"`
		MemoryMB int    `json:"memory_mb"`
		Region   string `json:"region"`
		Product  string `json:"product"`
	}
}

// skuEntry is how kario skus -json lists one (Kario Studio's picker).
type skuEntry struct {
	ID       string `json:"id"`
	Device   string `json:"device"`
	Form     string `json:"form"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	MemoryMB int    `json:"memory_mb"`
	Board    string `json:"board"`
	Display  string `json:"display"`
	Keypad   string `json:"keypad"`
	Product  string `json:"product"`
	Region   string `json:"region"`
	Emulator bool   `json:"emulator"`
}

func loadSKUs() ([]simSKU, error) {
	var out []simSKU
	if err := json.Unmarshal(skusJSON, &out); err != nil {
		return nil, fmt.Errorf("skus.json: %w", err)
	}
	for i := range out {
		if err := json.Unmarshal(out[i].Shell, &out[i].info); err != nil {
			return nil, fmt.Errorf("skus.json: %s: %w", out[i].ID, err)
		}
	}
	return out, nil
}

func (s simSKU) entry() skuEntry {
	return skuEntry{ID: s.ID, Device: s.info.Device, Form: s.Form, Width: s.Width, Height: s.Height, MemoryMB: s.info.MemoryMB,
		Board: s.info.Board, Display: s.info.Display, Keypad: s.info.Keypad, Product: s.info.Product, Region: s.info.Region, Emulator: s.Emulator}
}

func skuByID(id string) (simSKU, error) {
	all, err := loadSKUs()
	if err != nil {
		return simSKU{}, err
	}
	var ids []string
	for _, s := range all {
		if s.ID == id {
			return s, nil
		}
		ids = append(ids, s.ID)
	}
	return simSKU{}, fmt.Errorf("-sku %q: not a SKU (kario skus: %s)", id, strings.Join(ids, ", "))
}

// listSKUs prints the SKUs, as a table or (asJSON) a JSON array.
func listSKUs(asJSON bool) error {
	all, err := loadSKUs()
	if err != nil {
		return err
	}
	var entries []skuEntry
	for _, s := range all {
		entries = append(entries, s.entry())
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SKU\tFORM FACTOR\tSCREEN\tKEYPAD\tMEMORY\tBOARD")
	for _, e := range entries {
		board := e.Board
		if e.Emulator {
			board += " (emulator)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%dx%d\t%s\t%d MB\t%s\n", e.ID, e.Device, e.Width, e.Height, e.Form, e.MemoryMB, board)
	}
	return tw.Flush()
}

// writeSKU writes the sku.json phone-shell --sku reads for s, in dir.
func writeSKU(s simSKU, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, s.ID+".json")
	return p, os.WriteFile(p, append(slices.Clone(s.Shell), '\n'), 0o644)
}
