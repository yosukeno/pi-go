// Test program to verify RFC B1 overhead metrics implementation
package main

import (
	"context"
	"fmt"

	"github.com/yosukeno/pi-go/agent"
	"github.com/yosukeno/pi-go/llm"
	"github.com/yosukeno/pi-go/tools"
)

// mockClient is a simple test client that returns a fixed response
type mockClient struct{}

func (m *mockClient) Model() string { return "test-model" }

func (m *mockClient) Stream(
	ctx context.Context,
	systemPrompt string,
	history []llm.Message,
	tools []llm.ToolSchema,
	onDelta func(llm.Delta),
) (llm.Response, error) {
	// Simulate a response with some token usage
	return llm.Response{
		Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.Block{{Type: llm.BlockText, Text: "Test response"}},
		},
		Usage: llm.Usage{
			Input:  2000, // Simulate input tokens
			Output: 100,  // Simulate output tokens
		},
		StopReason: llm.StopEndTurn,
	}, nil
}

func main() {
	fmt.Println("RFC B1 Overhead Metrics Test")
	fmt.Println("============================")

	// Create a tool registry
	registry := tools.NewRegistry(
		&tools.Bash{},
		&tools.Edit{},
		&tools.Find{},
		&tools.Grep{},
		&tools.Ls{},
		&tools.Read{},
		&tools.Write{},
	)

	systemPrompt := agent.SystemPrompt("/tmp/test")

	// Create an agent with the mock client
	agent := agent.New(agent.Config{
		Client:       &mockClient{},
		Registry:     registry,
		SystemPrompt: systemPrompt,
		MaxTurns:     1,
	})

	// Run a simple prompt
	ctx := context.Background()
	eventChan := agent.Run(ctx, "test prompt")

	// Collect events
	var overheadFound bool
	for event := range eventChan {
		if event.OverheadMetrics != nil {
			overheadFound = true
			fmt.Printf("\nOverhead Metrics Found:\n")
			fmt.Printf("  Fixed Cost Tokens:    %d\n", event.OverheadMetrics.FixedCostTokens)
			fmt.Printf("  Total Input Tokens:    %d\n", event.OverheadMetrics.TotalInputTokens)
			fmt.Printf("  Overhead Ratio:       %.2f%%\n", event.OverheadMetrics.OverheadRatio*100)
			fmt.Printf("  Event Type:           %s\n", event.Kind)
		}

		if string(event.Kind) == "agent_end" {
			fmt.Printf("\nAgent run completed.\n")
			if event.OverheadMetrics != nil {
				fmt.Printf("  Final Overhead Ratio: %.2f%%\n", event.OverheadMetrics.OverheadRatio*100)
			}
		}
	}

	if !overheadFound {
		fmt.Println("\n⚠ No overhead metrics found in events")
	} else {
		fmt.Println("\n✓ Overhead metrics successfully implemented!")
	}

	// Show the fixed cost breakdown
	fmt.Printf("\nFixed Cost Breakdown:\n")
	fmt.Printf("  System Prompt: ~%d bytes → ~%d tokens\n", len(systemPrompt), len(systemPrompt)/4)
	fmt.Printf("  Tool Schemas:  ~4474 bytes → ~1118 tokens (from measurement)\n")
	fmt.Printf("  Total Fixed:   ~%d tokens\n", (len(systemPrompt)/4)+1118)
}
