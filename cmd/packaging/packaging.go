package packaging

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"text/template"

	"github.com/otiai10/copy"

	"github.com/go-flutter-desktop/hover/internal/androidmanifest"
	"github.com/go-flutter-desktop/hover/internal/build"
	"github.com/go-flutter-desktop/hover/internal/config"
	"github.com/go-flutter-desktop/hover/internal/fileutils"
	"github.com/go-flutter-desktop/hover/internal/log"
	"github.com/go-flutter-desktop/hover/internal/pubspec"
)

var packagingPath = filepath.Join(build.BuildPath, "packaging")

func packagingFormatPath(packagingFormat string) string {
	directoryPath, err := filepath.Abs(filepath.Join(packagingPath, packagingFormat))
	if err != nil {
		log.Errorf("Failed to resolve absolute path for %s directory: %v", packagingFormat, err)
		os.Exit(1)
	}
	return directoryPath
}

func createPackagingFormatDirectory(packagingFormat string) {
	if _, err := os.Stat(packagingFormatPath(packagingFormat)); !os.IsNotExist(err) {
		log.Errorf("A file or directory named `%s` already exists. Cannot continue packaging init for %s.", packagingFormat, packagingFormat)
		os.Exit(1)
	}
	err := os.MkdirAll(packagingFormatPath(packagingFormat), 0775)
	if err != nil {
		log.Errorf("Failed to create %s directory %s: %v", packagingFormat, packagingFormatPath(packagingFormat), err)
		os.Exit(1)
	}
}

func getTemporaryBuildDirectory(projectName string, packagingFormat string) string {
	tmpPath, err := ioutil.TempDir("", "hover-build-"+projectName+"-"+packagingFormat)
	if err != nil {
		log.Errorf("Couldn't get temporary build directory: %v", err)
		os.Exit(1)
	}
	return tmpPath
}

func runPackaging(path string, command string) {
	bashCmd := exec.Command("bash", "-c", command)
	bashCmd.Stderr = os.Stderr
	bashCmd.Stdout = os.Stdout
	bashCmd.Dir = path
	err := bashCmd.Run()
	if err != nil {
		log.Warnf("Packaging is very experimental and has only been tested on Linux.")
		log.Infof("To help us debuging this error, please zip the content of:\n       \"%s\"\n       %s",
			log.Au().Blue(path),
			log.Au().Green("and try to package on another OS. You can also share this zip with the go-flutter team."))
		log.Infof("You can package the app without hover by running:")
		log.Infof("  `%s`", log.Au().Magenta("cd "+path))
		log.Infof("  executed command: `%s`", log.Au().Magenta(bashCmd.String()))
		os.Exit(1)
	}
}

var templateData map[string]string
var once sync.Once

func (t *packagingTask) getTemplateData(projectName, buildVersion string) map[string]string {
	once.Do(func() {
		templateData = map[string]string{
			"projectName":      projectName,
			"version":          buildVersion,
			"release":          strings.Split(buildVersion, ".")[0],
			"arch":             runtime.GOARCH,
			"description":      pubspec.GetPubSpec().GetDescription(),
			"organizationName": androidmanifest.AndroidOrganizationName(),
			"author":           pubspec.GetPubSpec().GetAuthor(),
			"applicationName":  config.GetConfig().GetApplicationName(projectName),
			"executableName":   config.GetConfig().GetExecutableName(projectName),
			"packageName":      config.GetConfig().GetPackageName(projectName),
			"license":          config.GetConfig().GetLicense(),
		}
		templateData["iconPath"] = executeStringTemplate(t.linuxDesktopFileIconPath, templateData)
		templateData["executablePath"] = executeStringTemplate(t.linuxDesktopFileExecutablePath, templateData)
	})
	return templateData
}

type packagingTask struct {
	packagingFormatName            string                         // Name of the packaging format: OS-TYPE
	dependsOn                      map[*packagingTask]string      // Packaging tasks this task depends on
	templateFiles                  map[string]string              // Template files to copy over on init
	executableFiles                []string                       // Files that should be executable
	linuxDesktopFileExecutablePath string                         // Path of the executable for linux .desktop file (only set on linux)
	linuxDesktopFileIconPath       string                         // Path of the icon for linux .desktop file (only set on linux)
	generateBuildFiles             func(packageName, path string) // Generate dynamic build files. Operates in the temporary directory
	buildOutputDirectory           string                         // Path to copy the build output of the app to. Operates in the temporary directory
	packagingScriptTemplate        string                         // Template for the command that actually packages the app
	outputFileExtension            string                         // File extension of the packaged app
	// NOTE: outputFileContainsVersion is currently always true, we could
	// consider adding a flag for it to let users disable it.
	outputFileContainsVersion bool // Whether the output file name contains the version
	// NOTE: outputFileUsesApplicationName is always true for darwin-* and
	// windows-*, and always false for linux-*. We could consider adding a flag
	// for it to enable and disable at will (defaulting to how it's currently
	// configured).
	outputFileUsesApplicationName bool // Uses the application name instead of the package name
	skipAssertInitialized         bool // Set to true when a task doesn't need to be initialized.
}

func (t *packagingTask) Name() string {
	return strings.SplitN(t.packagingFormatName, "-", 2)[1]
}

func (t *packagingTask) Init() {
	t.init(false)
}

func (t *packagingTask) init(ignoreAlreadyExists bool) {
	for task := range t.dependsOn {
		task.init(true)
	}
	if !t.IsInitialized() {
		createPackagingFormatDirectory(t.packagingFormatName)
		dir := packagingFormatPath(t.packagingFormatName)
		for sourceFile, destinationFile := range t.templateFiles {
			destinationFile = filepath.Join(dir, destinationFile)
			err := os.MkdirAll(filepath.Dir(destinationFile), 0775)
			if err != nil {
				log.Errorf("Failed to create directory %s: %v", filepath.Dir(destinationFile), err)
				os.Exit(1)
			}
			fileutils.CopyAsset(fmt.Sprintf("packaging/%s", sourceFile), destinationFile, fileutils.AssetsBox())
		}
		log.Infof("go/packaging/%s has been created. You can modify the configuration files and add it to git.", t.packagingFormatName)
		log.Infof(fmt.Sprintf("You now can package the %s using `%s`", strings.Split(t.packagingFormatName, "-")[0], log.Au().Magenta("hover build "+t.packagingFormatName)))
	} else if !ignoreAlreadyExists {
		log.Errorf("%s is already initialized for packaging.", t.packagingFormatName)
		os.Exit(1)
	}
}

func (t *packagingTask) Pack(buildVersion string) {
	for task := range t.dependsOn {
		task.Pack(buildVersion)
	}
	projectName := pubspec.GetPubSpec().Name
	tmpPath := getTemporaryBuildDirectory(projectName, t.packagingFormatName)
	defer func() {
		err := os.RemoveAll(tmpPath)
		if err != nil {
			log.Errorf("Could not remove temporary build directory: %v", err)
			os.Exit(1)
		}
	}()
	log.Infof("Packaging %s in %s", strings.Split(t.packagingFormatName, "-")[1], tmpPath)

	if t.buildOutputDirectory != "" {
		err := copy.Copy(build.OutputDirectoryPath(strings.Split(t.packagingFormatName, "-")[0]), executeStringTemplate(filepath.Join(tmpPath, t.buildOutputDirectory), t.getTemplateData(projectName, buildVersion)))
		if err != nil {
			log.Errorf("Could not copy build folder: %v", err)
			os.Exit(1)
		}
	}
	for task, destination := range t.dependsOn {
		err := copy.Copy(build.OutputDirectoryPath(task.packagingFormatName), filepath.Join(tmpPath, destination))
		if err != nil {
			log.Errorf("Could not copy build folder of %s: %v", task.packagingFormatName, err)
			os.Exit(1)
		}
	}
	fileutils.CopyTemplateDir(packagingFormatPath(t.packagingFormatName), filepath.Join(tmpPath), t.getTemplateData(projectName, buildVersion))
	if t.generateBuildFiles != nil {
		log.Infof("Generating dynamic build files")
		t.generateBuildFiles(config.GetConfig().GetPackageName(projectName), tmpPath)
	}

	for _, file := range t.executableFiles {
		err := os.Chmod(executeStringTemplate(filepath.Join(tmpPath, file), t.getTemplateData(projectName, buildVersion)), 0777)
		if err != nil {
			log.Errorf("Failed to change file permissions for %s file: %v", file, err)
			os.Exit(1)
		}
	}

	err := os.RemoveAll(build.OutputDirectoryPath(t.packagingFormatName))
	log.Printf("Cleaning the build directory")
	if err != nil {
		log.Errorf("Failed to clean output directory %s: %v", build.OutputDirectoryPath(t.packagingFormatName), err)
		os.Exit(1)
	}

	packagingScript := executeStringTemplate(t.packagingScriptTemplate, t.getTemplateData(projectName, buildVersion))
	runPackaging(tmpPath, packagingScript)
	var outputFileName string
	if t.outputFileUsesApplicationName {
		outputFileName += config.GetConfig().GetApplicationName(projectName)
	} else {
		outputFileName += config.GetConfig().GetPackageName(projectName)
	}
	if t.outputFileContainsVersion {
		if t.outputFileUsesApplicationName {
			outputFileName += " "
		} else {
			outputFileName += "-"
		}
		outputFileName += buildVersion
	}
	outputFileName += "." + t.outputFileExtension
	outputFilePath := executeStringTemplate(filepath.Join(build.OutputDirectoryPath(t.packagingFormatName), outputFileName), t.getTemplateData(projectName, buildVersion))
	err = copy.Copy(filepath.Join(tmpPath, outputFileName), outputFilePath)
	if err != nil {
		log.Errorf("Could not move %s file: %v", outputFileName, err)
		os.Exit(1)
	}
}

func (t *packagingTask) AssertInitialized() {
	if t.skipAssertInitialized {
		return
	}
	if !t.IsInitialized() {
		log.Errorf("%s is not initialized for packaging. Please run `hover init-packaging %s` first.", t.packagingFormatName, t.packagingFormatName)
		os.Exit(1)
	}
}

func (t *packagingTask) IsInitialized() bool {
	_, err := os.Stat(packagingFormatPath(t.packagingFormatName))
	return !os.IsNotExist(err)
}

func executeStringTemplate(t string, data map[string]string) string {
	tmplFile, err := template.New("").Option("missingkey=error").Parse(t)
	if err != nil {
		log.Errorf("Failed to parse template string: %v\n", err)
		os.Exit(1)
	}
	var tmplBytes bytes.Buffer
	err = tmplFile.Execute(&tmplBytes, data)
	if err != nil {
		panic(err)
	}
	return tmplBytes.String()
}
