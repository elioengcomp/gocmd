package cmd

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	gofrogio "github.com/jfrog/gofrog/io"
	"github.com/jfrog/jfrog-client-go/utils"
	"github.com/jfrog/jfrog-client-go/utils/errorutils"
	"github.com/jfrog/jfrog-client-go/utils/io/fileutils"
	"github.com/jfrog/jfrog-client-go/utils/log"
)

var (
	moduleEntryInSumFileRegexp = regexp.MustCompile(`([^\s]+) (v[^\/]+)\/go\.mod`)
)

func prepareRegExp() error {
	err := prepareGlobalRegExp()
	if err != nil {
		return err
	}
	return prepareNotFoundZipRegExp()
}

// Compiles all the regex once
func prepareGlobalRegExp() error {
	var err error
	if protocolRegExp == nil {
		log.Debug("Initializing protocol regexp")
		protocolRegExp, err = initRegExp(utils.CredentialsInUrlRegexp, MaskCredentials)
		if err != nil {
			return err
		}
	}

	if notFoundRegExp == nil {
		log.Debug("Initializing not found regexp")
		notFoundRegExp, err = initRegExp(`^go: ([^\/\r\n]+\/[^\r\n\s:]*).*(404 Not Found[\s]?)$`, Error)
		if err != nil {
			return err
		}
	}

	if unrecognizedImportRegExp == nil {
		log.Debug("Initializing unrecognized import path regexp")
		unrecognizedImportRegExp, err = initRegExp(`[^go:]([^\/\r\n]+\/[^\r\n\s:]*).*(unrecognized import path)`, Error)
		if err != nil {
			return err
		}
	}

	if unknownRevisionRegExp == nil {
		log.Debug("Initializing unknown revision regexp")
		unknownRevisionRegExp, err = initRegExp(`[^go:]([^\/\r\n]+\/[^\r\n\s:]*).*(unknown revision)`, Error)
	}

	if gitFetchErrorRegExp == nil {
		log.Debug("Initializing git fetch error regexp")
		gitFetchErrorRegExp, err = initRegExp(`^go: ([^:]+): git fetch .+ (exit status [^0]\d*)`, Error)
	}

	return err
}

func prepareNotFoundZipRegExp() error {
	var err error
	if notFoundZipRegExp == nil {
		log.Debug("Initializing not found zip file")
		notFoundZipRegExp, err = initRegExp(`unknown import path ["]([^\/\r\n]+\/[^\r\n\s:]*)["].*(404( Not Found)?[\s]?)$`, Error)
	}
	return err
}

func initRegExp(regex string, execFunc func(pattern *gofrogio.CmdOutputPattern) (string, error)) (*gofrogio.CmdOutputPattern, error) {
	regExp, err := utils.GetRegExp(regex)
	if err != nil {
		return &gofrogio.CmdOutputPattern{}, err
	}

	outputPattern := &gofrogio.CmdOutputPattern{
		RegExp: regExp,
	}

	outputPattern.ExecFunc = execFunc
	return outputPattern, nil
}

// Mask the credentials information from the line.
func MaskCredentials(pattern *gofrogio.CmdOutputPattern) (string, error) {
	return utils.MaskCredentials(pattern.Line, pattern.MatchedResults[0]), nil
}

func Error(pattern *gofrogio.CmdOutputPattern) (string, error) {
	_, err := fmt.Fprint(os.Stderr, pattern.Line)
	if err != nil {
		return "", errorutils.CheckError(err)
	}
	if len(pattern.MatchedResults) >= 3 {
		return "", errors.New(pattern.MatchedResults[2] + ":" + strings.TrimSpace(pattern.MatchedResults[1]))
	}
	return "", errors.New(fmt.Sprintf("Regex found the following values: %s", pattern.MatchedResults))
}

func GetSumContentAndRemove(rootProjectDir string) (sumFileContent []byte, sumFileStat os.FileInfo, err error) {
	sumFileExists, err := fileutils.IsFileExists(filepath.Join(rootProjectDir, "go.sum"), false)
	if err != nil {
		return
	}
	if sumFileExists {
		log.Debug("Sum file exists:", rootProjectDir)
		sumFileContent, sumFileStat, err = GetFileDetails(filepath.Join(rootProjectDir, "go.sum"))
		if err != nil {
			return
		}
		log.Debug("Removing file:", filepath.Join(rootProjectDir, "go.sum"))
		err = os.Remove(filepath.Join(rootProjectDir, "go.sum"))
		if err != nil {
			return
		}
		return
	}
	return
}

func PrintGoSumContent(rootProjectDir string) error {
	log.Debug("Checking go.sum content")
	sumFileExists, err := fileutils.IsFileExists(filepath.Join(rootProjectDir, "go.sum"), false)
	if err != nil {
		return err
	}
	if sumFileExists {
		sumFileContent, _, err := GetFileDetails(filepath.Join(rootProjectDir, "go.sum"))
		if err != nil {
			return err
		}
		log.Debug("Sum file content:", string(sumFileContent))
	}
	return nil
}

func FetchModulesFromGoSum(rootProjectDir string) ([]string, error) {
	log.Debug("Fetching go modules declared in go.sum")
	var modules []string
	sumFileExists, err := fileutils.IsFileExists(filepath.Join(rootProjectDir, "go.sum"), false)
	if err != nil {
		return nil, err
	}
	if sumFileExists {
		sumFileContent, _, err := GetFileDetails(filepath.Join(rootProjectDir, "go.sum"))
		if err != nil {
			return nil, err
		}

		scanner := bufio.NewScanner(bytes.NewReader(sumFileContent))
		for scanner.Scan() {
			matches := moduleEntryInSumFileRegexp.FindStringSubmatch(scanner.Text())
			if len(matches) > 0 {
				modules = append(modules, fmt.Sprintf("%s@%s", matches[1], matches[2]))
			}
		}
	}
	return modules, nil
}

func RestoreSumFile(rootProjectDir string, sumFileContent []byte, sumFileStat os.FileInfo) error {
	log.Debug("Restoring file:", filepath.Join(rootProjectDir, "go.sum"))
	err := ioutil.WriteFile(filepath.Join(rootProjectDir, "go.sum"), sumFileContent, sumFileStat.Mode())
	if err != nil {
		return err
	}
	return nil
}

func GetFileDetails(filePath string) (modFileContent []byte, modFileStat os.FileInfo, err error) {
	modFileStat, err = os.Stat(filePath)
	if errorutils.CheckError(err) != nil {
		return
	}
	modFileContent, err = ioutil.ReadFile(filePath)
	errorutils.CheckError(err)
	return
}

func modulesToMap(modules []string) map[string]bool {
	mapOfDeps := map[string]bool{}
	for _, module := range modules {
		mapOfDeps[module] = true
	}
	return mapOfDeps
}

func outputToMap(output string, errorOutput string) map[string]bool {
	// Parse dependency graph output
	lineOutput := strings.Split(output, "\n")
	mapOfDeps := map[string]bool{}
	for _, line := range lineOutput {
		splitLine := strings.Split(line, " ")
		if len(splitLine) == 2 {
			mapOfDeps[splitLine[1]] = true
		}
	}

	// Parse dependency resolution output, sent to the error output by go mod graph
	lineOutput = strings.Split(errorOutput, "\n")
	for _, line := range lineOutput {
		if strings.HasPrefix(line, "go: finding") {
			lineContent := line[12:]
			dependencyParts := strings.Split(lineContent, " ")
			dependency := fmt.Sprintf("%s@%s", dependencyParts[0], dependencyParts[1])
			mapOfDeps[dependency] = true
		}
	}

	return mapOfDeps
}
