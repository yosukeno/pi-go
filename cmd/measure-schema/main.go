// Command measure-schema calculates the exact wire-format size of tool schemas.
// This is used for RFC B1: Fixed Overhead Calculation.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/yosukeno/pi-go/tools"
)

func main() {
	// Create a registry with all built-in tools
	registry := tools.NewRegistry(
		&tools.Bash{},
		&tools.Edit{},
		&tools.Find{},
		&tools.Grep{},
		&tools.Ls{},
		&tools.Read{},
		&tools.Write{},
	)

	// Collect all tool schemas (same as agent does)
	schemas := make([]toolSchema, 0, len(registry.All()))
	for _, t := range registry.All() {
		schemas = append(schemas, toolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}

	// Convert to wire format (mimicking llm/convert.go)
	wireTools := toWireTools(schemas)
	wireJSON, err := json.Marshal(wireTools)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling tools: %v\n", err)
		os.Exit(1)
	}

	// Calculate individual tool sizes
	totalBytes := 0
	fmt.Println("Tool Schema Size Analysis")
	fmt.Println("========================")
	fmt.Printf("%-20s %-10s %-10s %-15s\n", "Tool Name", "Bytes", "Desc", "Total")
	fmt.Println("------------------------------------------------")

	for _, t := range schemas {
		// Marshal single tool to wire format
		singleTool := []toolSchema{t}
		wireSingle := toWireTools(singleTool)
		singleJSON, err := json.Marshal(wireSingle)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling %s: %v\n", t.Name, err)
			continue
		}

		bytes := len(singleJSON)
		totalBytes += bytes
		fmt.Printf("%-20s %-10d %-10d %-15d\n", t.Name, bytes, len(t.Description), bytes)
	}

	fmt.Println("------------------------------------------------")
	fmt.Printf("%-20s %-10d\n", "TOTAL", totalBytes)

	// Full wire output analysis
	fmt.Println("\nWire Format Output")
	fmt.Println("==================")
	fmt.Printf("Full JSON size: %d bytes\n", len(wireJSON))
	fmt.Printf("Tool count: %d\n", len(schemas))

	// Estimate tokens (rough approximation: 4 chars per token)
	estimatedTokens := len(wireJSON) / 4
	fmt.Printf("Estimated tokens: ~%d\n", estimatedTokens)

	// Show sample of the wire format
	fmt.Println("\nSample wire format (first 300 chars):")
	if len(wireJSON) > 300 {
		fmt.Printf("%s...\n", string(wireJSON[:300]))
	} else {
		fmt.Printf("%s\n", string(wireJSON))
	}

	// Compare with system prompt size from RFC
	fmt.Println("\nRFC B1 Comparison")
	fmt.Println("=================")
	systemPromptSize := 739 // from RFC
	fmt.Printf("System prompt size: %d bytes (from RFC)\n", systemPromptSize)
	fmt.Printf("Tool schemas size: %d bytes\n", totalBytes)
	ratio := float64(totalBytes) / float64(systemPromptSize)
	fmt.Printf("Ratio (tools/system): %.1fx\n", ratio)
	fmt.Printf("Expected ratio: 5.8x (from RFC)\n")
	if ratio > 5.5 && ratio < 6.1 {
		fmt.Println("[PASS] Ratio matches RFC expectations!")
	} else {
		fmt.Printf("[WARN] Ratio differs from RFC (5.8x)\n")
	}
}

// toolSchema matches llm.ToolSchema but without external dependency
type toolSchema struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// wireToolSchema matches llm.wireToolSchema
type wireToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

// wireTool matches llm.wireTool
type wireTool struct {
	Type     string         `json:"type"`
	Function wireToolSchema `json:"function"`
}

// toWireTools converts tool schemas to the wire format used by the API.
// Copied from llm/convert.go to maintain consistency.
func toWireTools(tools []toolSchema) []wireTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]wireTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, wireTool{
			Type: "function",
			Function: wireToolSchema{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	return out
}
