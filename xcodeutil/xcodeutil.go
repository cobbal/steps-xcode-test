package xcodeutil

import (
	"bufio"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/bitrise-io/go-utils/log"
	xcodeModels "github.com/bitrise-io/go-xcode/models"
	cmd "github.com/bitrise-steplib/steps-xcode-test/command"
	"github.com/bitrise-steplib/steps-xcode-test/models"
	ver "github.com/hashicorp/go-version"
)

//=======================================
// Utility
//=======================================

// a simulator info line should look like this:
//  iPhone 5s (EA1C7E48-8137-428C-A0A5-B2C63FF276EB) (Shutdown)
// or
//  iPhone 4s (51B10EBD-C949-49F5-A38B-E658F41640FF) (Shutdown) (unavailable, runtime profile not found)
func getSimInfoFromLine(lineStr string) (models.SimInfoModel, error) {
	baseInfosExp := regexp.MustCompile(`(?P<deviceName>[a-zA-Z].*[a-zA-Z0-9 -]*) \((?P<simulatorID>[a-zA-Z0-9-]{36})\) \((?P<status>[a-zA-Z]*)\)`)
	baseInfosRes := baseInfosExp.FindStringSubmatch(lineStr)
	if baseInfosRes == nil {
		return models.SimInfoModel{}, fmt.Errorf("No match found")
	}

	simInfo := models.SimInfoModel{
		Name:   baseInfosRes[1],
		SimID:  baseInfosRes[2],
		Status: baseInfosRes[3],
	}

	// StatusOther
	restOfTheLine := lineStr[len(baseInfosRes[0]):]
	if len(restOfTheLine) > 0 {
		statusOtherExp := regexp.MustCompile(`\((?P<statusOther>[a-zA-Z ,]*)\)`)
		statusOtherRes := statusOtherExp.FindStringSubmatch(restOfTheLine)
		if statusOtherRes != nil {
			simInfo.StatusOther = statusOtherRes[1]
		}
	}
	return simInfo, nil
}

func collectAllSimIDs(simctlListOutputToScan string) models.SimulatorsGroupedByIOSVersionsModel {
	simulatorsByIOSVersions := models.SimulatorsGroupedByIOSVersionsModel{}
	currIOSVersion := ""

	fscanner := bufio.NewScanner(strings.NewReader(simctlListOutputToScan))
	isDevicesSectionFound := false
	for fscanner.Scan() {
		aLine := fscanner.Text()

		if aLine == "== Devices ==" {
			isDevicesSectionFound = true
			continue
		}

		if !isDevicesSectionFound {
			continue
		}
		if strings.HasPrefix(aLine, "==") {
			isDevicesSectionFound = false
			continue
		}
		if strings.HasPrefix(aLine, "--") {
			iosVersionSectionExp := regexp.MustCompile(`-- (?P<iosVersionSection>.*) --`)
			iosVersionSectionRes := iosVersionSectionExp.FindStringSubmatch(aLine)
			if iosVersionSectionRes != nil {
				currIOSVersion = iosVersionSectionRes[1]
			}
			continue
		}

		// fmt.Println("-> ", aLine)
		simInfo, err := getSimInfoFromLine(aLine)
		if err != nil {
			fmt.Println(" [!] Error scanning the line for Simulator info: ", err)
		}

		currIOSVersionSimList := simulatorsByIOSVersions[currIOSVersion]
		currIOSVersionSimList = append(currIOSVersionSimList, simInfo)
		simulatorsByIOSVersions[currIOSVersion] = currIOSVersionSimList
	}

	return simulatorsByIOSVersions
}

// Compares sematic versions with 2 components (9.1, 9.2, ...)
// Return true if first version is greater then second
func isOsVersionGreater(osVersion, otherOsVersion string) (bool, error) {
	versionPtrs := []*ver.Version{}
	for _, osVer := range []string{osVersion, otherOsVersion} {
		osVersionSplit := strings.Split(osVer, " ")
		if len(osVersionSplit) != 2 {
			return false, fmt.Errorf("failed to parse version: %s", osVer)
		}

		version, err := ver.NewVersion(osVersionSplit[1])
		if err != nil {
			return false, err
		}

		versionPtrs = append(versionPtrs, version)
	}

	return versionPtrs[0].GreaterThan(versionPtrs[1]), nil
}

func getLatestOsVersion(desiredPlatform, desiredDevice string, allSimIDsGroupedBySimVersion models.SimulatorsGroupedByIOSVersionsModel) (string, error) {
	latestOsVersion := ""
	for osVersion, simInfos := range allSimIDsGroupedBySimVersion {
		if !strings.HasPrefix(osVersion, desiredPlatform) {
			continue
		}

		deviceExist := false
		for _, simInfo := range simInfos {
			if simInfo.Name == desiredDevice {
				deviceExist = true
				break
			}
		}

		if !deviceExist {
			continue
		}

		if latestOsVersion == "" {
			latestOsVersion = osVersion
		} else {
			greater, err := isOsVersionGreater(osVersion, latestOsVersion)
			if err != nil {
				return "", err
			}

			if greater {
				latestOsVersion = osVersion
			}
		}
	}

	if latestOsVersion == "" {
		return "", fmt.Errorf("failed to find latest os version for (%s) - (%s)", desiredPlatform, desiredDevice)
	}

	return latestOsVersion, nil
}

//=======================================
// Main
//=======================================

// GetSimulator ...
func GetSimulator(simulatorPlatform, simulatorDevice, simulatorOsVersion string) (models.SimInfoModel, error) {
	cmd := exec.Command("xcrun", "simctl", "list")
	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		return models.SimInfoModel{}, err
	}

	simctlListOut := string(outBytes)

	allSimIDsGroupedBySimVersion := collectAllSimIDs(simctlListOut)

	//
	// map desired inputs
	simulatorPlatformSplit := strings.Split(simulatorPlatform, " Simulator")
	if len(simulatorPlatformSplit) == 0 {
		return models.SimInfoModel{}, fmt.Errorf("failed to parse simulator platform (%s)", simulatorPlatform)
	}

	if simulatorDevice == "iPad" {
		log.Warnf("Given device (%s) is deprecated, using (iPad 2)...", simulatorDevice)
		simulatorDevice = "iPad 2"
	}

	desiredPlatform := simulatorPlatformSplit[0]
	desiredOsVersion := ""

	if simulatorOsVersion == "latest" {
		latestOsVersion, err := getLatestOsVersion(desiredPlatform, simulatorDevice, allSimIDsGroupedBySimVersion)
		if err != nil {
			return models.SimInfoModel{}, fmt.Errorf("failed to get latest os version, error: %s", err)
		}
		desiredOsVersion = latestOsVersion
	} else {
		normalizedOsVersion := simulatorOsVersion
		osVersionSplit := strings.Split(normalizedOsVersion, ".")
		if len(osVersionSplit) > 2 {
			normalizedOsVersion = strings.Join(osVersionSplit[0:2], ".")
		}
		desiredOsVersion = fmt.Sprintf("%s %s", desiredPlatform, normalizedOsVersion)
	}

	//
	// find desired simulator
	simInfoList, found := allSimIDsGroupedBySimVersion[desiredOsVersion]
	if !found {
		return models.SimInfoModel{}, fmt.Errorf("no simulator found for desired os: %s", desiredOsVersion)
	}

	for _, simInfo := range simInfoList {
		if simInfo.Name == simulatorDevice {
			return simInfo, nil
		}
	}

	return models.SimInfoModel{}, fmt.Errorf("%s - %s - %s not found", simulatorPlatform, simulatorDevice, simulatorOsVersion)
}

func getXcodeDeveloperDirPath() (string, error) {
	cmd := exec.Command("xcode-select", "--print-path")
	outBytes, err := cmd.CombinedOutput()
	outStr := string(outBytes)
	return strings.TrimSpace(outStr), err
}

// BootSimulator ...
func BootSimulator(simulator models.SimInfoModel, xcodebuildVersion xcodeModels.XcodebuildVersionModel) error {
	simulatorApp := "Simulator"
	if xcodebuildVersion.MajorVersion == 6 {
		simulatorApp = "iOS Simulator"
	}
	xcodeDevDirPth, err := getXcodeDeveloperDirPath()
	if err != nil {
		return fmt.Errorf("Failed to get Xcode Developer Directory - most likely Xcode.app is not installed")
	}
	simulatorAppFullPath := filepath.Join(xcodeDevDirPth, "Applications", simulatorApp+".app")

	openCmd := exec.Command("open", simulatorAppFullPath, "--args", "-CurrentDeviceUDID", simulator.SimID)

	log.Printf("$ %s", cmd.PrintableCommandArgs(openCmd.Args))

	out, err := openCmd.CombinedOutput()
	outStr := string(out)
	if err != nil {
		return fmt.Errorf("failed to start simulators (%s), output: %s, error: %s", simulator.SimID, outStr, err)
	}

	return nil
}

// GetXcodeVersion ...
func GetXcodeVersion() (xcodeModels.XcodebuildVersionModel, error) {
	cmd := exec.Command("xcodebuild", "-version")
	outBytes, err := cmd.CombinedOutput()
	outStr := string(outBytes)
	if err != nil {
		return xcodeModels.XcodebuildVersionModel{}, fmt.Errorf("xcodebuild -version failed, err: %s, details: %s", err, outStr)
	}

	split := strings.Split(outStr, "\n")
	if len(split) == 0 {
		return xcodeModels.XcodebuildVersionModel{}, fmt.Errorf("failed to parse xcodebuild version output (%s)", outStr)
	}

	xcodebuildVersion := split[0]
	buildVersion := split[1]

	split = strings.Split(xcodebuildVersion, " ")
	if len(split) != 2 {
		return xcodeModels.XcodebuildVersionModel{}, fmt.Errorf("failed to parse xcodebuild version output (%s)", outStr)
	}

	version := split[1]

	split = strings.Split(version, ".")
	majorVersionStr := split[0]

	majorVersion, err := strconv.ParseInt(majorVersionStr, 10, 32)
	if err != nil {
		return xcodeModels.XcodebuildVersionModel{}, fmt.Errorf("failed to parse xcodebuild version output (%s), error: %s", outStr, err)
	}

	return xcodeModels.XcodebuildVersionModel{
		Version:      xcodebuildVersion,
		BuildVersion: buildVersion,
		MajorVersion: majorVersion,
	}, nil
}
