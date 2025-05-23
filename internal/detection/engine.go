package detection

import (
	"bytes"
	"embed"
	"fmt"
	"github.com/mcpguard/mcpguard/internal/mcp"
	"github.com/spf13/viper"
	"github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/detect"
)

//go:embed gitleaks.toml
var configFS embed.FS

type Engine struct {
	detector *detect.Detector
}

// NewEngine creates a new detection engine with gitleaks initialized
func NewEngine() (*Engine, error) {
	// Setup viper to read the config file
	v := viper.New()
	v.SetConfigType("toml")

	// Set path to your local gitleaks.toml file
	configData, err := configFS.ReadFile("gitleaks.toml")
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded config: %w", err)
	}

	// Read the config from the configData bytes
	if err := v.ReadConfig(bytes.NewReader(configData)); err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	// Parse into gitleaks config format
	var vc config.ViperConfig
	if err := v.Unmarshal(&vc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Translate to GitLeaks config
	cfg, err := vc.Translate()
	if err != nil {
		return nil, fmt.Errorf("failed to translate config: %w", err)
	}

	// Create the detector with the parsed config
	detector := detect.NewDetector(cfg)

	return &Engine{
		detector: detector,
	}, nil
}

func (e *Engine) Detect(request mcp.Request) []Result {

	// create an empty result slice
	var results []Result

	for _, arg := range request.Params.Arguments {
		if argStr, ok := arg.(string); ok {
			detectResult := e.detector.DetectString(argStr)
			// append to results slice
			for _, res := range detectResult {
				results = append(results, Result{
					Description: res.Description,
				})
			}
		}
	}
	return results
}
