package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/QC3284/BBDown-Go/internal/util"
)

// SubCommandNames are the registered subcommands: their flags do not support
// the full download option set, so config merging is skipped for them.
var SubCommandNames = []string{"login", "logintv", "serve", "live", "article", "watchlater", "sub"}

var urlLikeToken = regexp.MustCompile(`(?i)^(https?://|av\d+|bv[0-9A-Za-z]+|av:|bv:|ep\d+|ep:|ss\d+|ss:|md\d+|md:|cheese[:/]|mid:|favId:|listBizId:|seriesBizId:)`)

// IsSubCommandInvocation reports whether the first positional token is a
// subcommand name (value-consuming options are skipped while scanning).
func IsSubCommandInvocation(args []string, aliasMap map[string]string, boolFlags map[string]bool) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			for _, name := range SubCommandNames {
				if strings.EqualFold(arg, name) {
					return true
				}
			}
			return false
		}
		if strings.Contains(arg, "=") {
			continue // value embedded in the token
		}
		if canonical, ok := aliasMap[arg]; ok && !boolFlags[canonical] {
			i++ // this option consumes the next token as its value
		}
	}
	return false
}

// positionalTokens returns the CLI tokens that are neither an option nor the
// value consumed by one. The URL heuristic must only look at these: scanning the
// whole argv misreads an option value such as "--aria2c-proxy http://127.0.0.1:1080"
// or "--work-dir av123" as the target URL, which then silently drops the real URL
// from BBDown.config and fails with "缺少参数" (upstream RF-7 GetPositionalTokens).
func positionalTokens(args []string, aliasMap map[string]string, boolFlags map[string]bool) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			out = append(out, arg)
			continue
		}
		if strings.Contains(arg, "=") {
			continue // value embedded in the token
		}
		if canonical, ok := aliasMap[arg]; ok && !boolFlags[canonical] {
			i++ // this option consumes the next token as its value
		}
	}
	return out
}

// MergeWithConfig merges a line-based BBDown.config file into the CLI args
// (upstream BBDownConfigParser): config options act as defaults, explicit CLI
// options win, and the config URL is dropped when the CLI already has one.
func MergeWithConfig(cliArgs []string, aliasMap map[string]string, boolFlags map[string]bool) ([]string, error) {
	result := append([]string(nil), cliArgs...)

	if IsSubCommandInvocation(cliArgs, aliasMap, boolFlags) {
		return result, nil
	}

	configPath := ""
	for i := 0; i < len(cliArgs); i++ {
		if cliArgs[i] == "--config-file" && i+1 < len(cliArgs) {
			configPath = cliArgs[i+1]
			break
		}
		if strings.HasPrefix(cliArgs[i], "--config-file=") {
			configPath = strings.TrimPrefix(cliArgs[i], "--config-file=")
			break
		}
	}
	if configPath == "" {
		configPath = filepath.Join(appDir(), "BBDown.config")
	}

	if _, err := os.Stat(configPath); err != nil {
		return result, nil
	}

	lines, err := os.ReadFile(configPath)
	if err != nil {
		return result, nil
	}
	var configArgs []string
	for _, line := range strings.Split(string(lines), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "-") && strings.Contains(line, " ") {
			idx := strings.Index(line, " ")
			configArgs = append(configArgs, line[:idx], strings.Trim(strings.TrimSpace(line[idx:]), "\""))
		} else {
			configArgs = append(configArgs, strings.Trim(line, "\""))
		}
	}

	cliHasURL := false
	for _, a := range positionalTokens(cliArgs, aliasMap, boolFlags) {
		if urlLikeToken.MatchString(a) {
			cliHasURL = true
			break
		}
	}

	explicitOptions := make(map[string]bool)
	for _, a := range cliArgs {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		token := a
		if idx := strings.Index(a, "="); idx > 0 {
			token = a[:idx]
		}
		if canonical, ok := aliasMap[token]; ok {
			explicitOptions[canonical] = true
		}
	}

	for i := 0; i < len(configArgs); {
		name := configArgs[i]
		if !strings.HasPrefix(name, "-") {
			if !cliHasURL {
				result = append(result, name)
			}
			i++
			continue
		}

		canonical, known := aliasMap[name]
		if !known {
			result = append(result, name)
			i++
			continue
		}

		if explicitOptions[canonical] {
			i++
			// Skip exactly this option's config value(s). The previous loop
			// collected tokens until the next known option, which also swallowed a
			// trailing positional URL: config "--cookie <value>\n<URL>" plus a CLI
			// --cookie override lost the URL from the merged args (upstream
			// BBDownConfigParser.CountOptionValueTokens).
			i += optionValueCount(configArgs, i, canonical, aliasMap, boolFlags)
			continue
		}

		result = append(result, name)
		i++
		n := optionValueCount(configArgs, i, canonical, aliasMap, boolFlags)
		result = append(result, configArgs[i:i+n]...)
		i += n
	}

	return result, nil
}

func isKnownOption(token string, aliasMap map[string]string) bool {
	_, ok := aliasMap[token]
	return ok
}

// optionValueCount returns how many tokens after index belong to the value of
// the option whose canonical name is canonical (upstream
// BBDownConfigParser.CountOptionValueTokens).
//
// A boolean option takes at most one explicit "true"/"false"; a value option
// takes exactly one token — even one starting with '-' (config values like
// "-access-token-value" must survive) — unless that token is itself a known
// option, i.e. the option was written without a value. Limiting the count to
// the real value token is what stops a trailing config URL from being consumed
// when the CLI overrides the same option.
func optionValueCount(args []string, index int, canonical string, aliasMap map[string]string, boolFlags map[string]bool) int {
	if index >= len(args) {
		return 0
	}
	if boolFlags[canonical] {
		v := strings.TrimSpace(args[index])
		if strings.EqualFold(v, "true") || strings.EqualFold(v, "false") {
			return 1
		}
		return 0
	}
	if isKnownOption(args[index], aliasMap) {
		return 0
	}
	return 1
}

// appDir returns the executable directory (config default location).
func appDir() string {
	return util.ExecutableDir()
}
