package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/magicwubiao/go-magic/internal/skills"
	"github.com/magicwubiao/go-magic/internal/tool"
	"github.com/magicwubiao/go-magic/pkg/config"
)

// Skill metadata for templates
type SkillTemplate struct {
	Name        string
	Description string
	Tags        []string
	Tools       []string
	Content     string
}

var (
	skillCreateForce bool
	skillInstallFrom string
	skillInstallURL  string
)

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Manage skills",
	Long: `Manage Magic Agent skills.

Skills are loaded from three levels:
  - Built-in skills: bundled with the application
  - Global skills: under your magic home directory (/skills)
  - Workspace skills: ./skills/ or .magic/skills/

Supports multiple formats:
  - SKILL.md with YAML frontmatter (recommended)
  - JSON (.json) - with name, description, content
  - Markdown (.md, .markdown) - content as skill
  - Text (.txt) - plain text as skill
  - Directory with manifest.json

Examples:
  magic skills list
  magic skills show <name>
  magic skills search <keyword>
  magic skills install <name>
  magic skills create <name>
  magic skills delete <name>
  magic skills match <input>`,
}

var skillsShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a skill",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsShow,
}

var skillsSearchCmd = &cobra.Command{
	Use:   "search <keyword>",
	Short: "Search skills by keyword",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsSearch,
}

var skillsInstallCmd = &cobra.Command{
	Use:   "install <name>",
	Short: "Install a skill",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsInstall,
}

var skillsCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new skill from template",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsCreate,
}

var skillsDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a skill",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsDelete,
}

var skillsMatchCmd = &cobra.Command{
	Use:   "match <input>",
	Short: "Find skills matching the input",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsMatch,
}

// Hub management commands
var skillsUninstallCmd = &cobra.Command{
	Use:   "uninstall <name>",
	Short: "Uninstall a hub skill",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsUninstall,
}

var skillsDisableCmd = &cobra.Command{
	Use:   "disable <name>",
	Short: "Disable a skill (won't be shown to agent)",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsDisable,
}

var skillsEnableCmd = &cobra.Command{
	Use:   "enable <name>",
	Short: "Enable a disabled skill",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsEnable,
}

var skillsHubCmd = &cobra.Command{
	Use:   "hub",
	Short: "Manage hub skills",
	Long: `Manage hub-installed skills.

Examples:
  magic skills hub list
  magic skills hub audit`,
}

var skillsHubListCmd = &cobra.Command{
	Use:   "list",
	Short: "List hub-installed skills",
	Run:   runSkillsHubList,
}

var skillsHubAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Show hub audit log",
	Run:   runSkillsHubAudit,
}

var skillsConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage skill configuration",
	Long: `Manage skill configuration (disabled skills, etc.)

Examples:
  magic skills config list
  magic skills config disabled`,
}

var skillsConfigListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configuration",
	Run:   runSkillsConfigList,
}

var skillsConfigDisabledCmd = &cobra.Command{
	Use:   "disabled",
	Short: "List disabled skills",
	Run:   runSkillsConfigDisabled,
}

// =============================================================================
// Auto-Skill Lifecycle Management (Three-State Management)
// =============================================================================

var skillsAutoCmd = &cobra.Command{
	Use:   "auto",
	Short: "Manage auto-generated skills (pending/approved/archived)",
	Long: `Manage auto-generated skills with a three-state lifecycle.

Automatically generated skills start in the "pending" state and are NOT
used by the Agent until you approve them.

States:
  pending   - waiting for manual review (not used by agent)
  approved  - approved and will be injected into agent prompts
  archived  - kept on disk but not active (can be restored)
  rejected  - rejected, pending deletion

Examples:
  magic skills auto list              # list all auto skills with status
  magic skills auto pending           # list pending skills
  magic skills auto approve <name>    # approve a pending skill
  magic skills auto reject <name>     # reject a pending skill
  magic skills auto archive <name>    # archive an approved skill
  magic skills auto restore <name>    # restore from archive
  magic skills auto delete <name>     # permanently delete a skill
  magic skills auto status <name>     # show current status
  magic skills auto stats             # show counts by status`,
}

var skillsAutoListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all auto-generated skills with their status",
	Args:  cobra.NoArgs,
	Run:   runSkillsAutoList,
}

var skillsAutoPendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "List pending auto skills (awaiting review)",
	Args:  cobra.NoArgs,
	Run:   runSkillsAutoPending,
}

var skillsAutoApproveCmd = &cobra.Command{
	Use:   "approve <name>",
	Short: "Approve a pending skill (it will now be used by agent)",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsAutoApprove,
}

var skillsAutoRejectCmd = &cobra.Command{
	Use:   "reject <name>",
	Short: "Reject a pending skill",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsAutoReject,
}

var skillsAutoArchiveCmd = &cobra.Command{
	Use:   "archive <name>",
	Short: "Archive an approved skill (kept but not used)",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsAutoArchive,
}

var skillsAutoRestoreCmd = &cobra.Command{
	Use:   "restore <name>",
	Short: "Restore an archived skill back to approved",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsAutoRestore,
}

var skillsAutoDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Permanently delete an auto skill (rejected/archived)",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsAutoDelete,
}

var skillsAutoStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show current status of an auto skill",
	Args:  cobra.ExactArgs(1),
	Run:   runSkillsAutoStatus,
}

var skillsAutoStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show counts of auto skills by status",
	Args:  cobra.NoArgs,
	Run:   runSkillsAutoStats,
}

// P1-2：技能版本查看与回滚。
// VersionManager 有能力做版本管理，但此前既没人写版本、也没有任何入口，
// 「技能被进化改坏」在 CLI 侧完全不可恢复。这两个命令是唯一的恢复通道。
var skillsVersionsCmd = &cobra.Command{
	Use:   "versions <name>",
	Short: "List version history of a skill",
	Long: `List all recorded versions of a skill.

Versions are recorded automatically when a skill is created, updated or
deleted, so this history is what makes "revert a skill that was broken by
evolution" possible.

Examples:
  magic skills versions code-review`,
	Args: cobra.ExactArgs(1),
	Run:  runSkillsVersions,
}

var skillsRollbackCmd = &cobra.Command{
	Use:   "rollback <name> <version>",
	Short: "Roll a skill back to a previous version",
	Long: `Restore a skill's SKILL.md to the content of a previous version.

Use "magic skills versions <name>" first to find the target version.

Examples:
  magic skills versions code-review
  magic skills rollback code-review 1.0`,
	Args: cobra.ExactArgs(2),
	Run:  runSkillsRollback,
}

func init() {
	skillsCmd.AddCommand(skillsShowCmd)
	skillsCmd.AddCommand(skillsSearchCmd)
	skillsCmd.AddCommand(skillsInstallCmd)
	skillsCmd.AddCommand(skillsUninstallCmd)
	skillsCmd.AddCommand(skillsCreateCmd)
	skillsCmd.AddCommand(skillsDeleteCmd)
	skillsCmd.AddCommand(skillsMatchCmd)
	skillsCmd.AddCommand(skillsDisableCmd)
	skillsCmd.AddCommand(skillsEnableCmd)
	skillsCmd.AddCommand(skillsHubCmd)
	skillsCmd.AddCommand(skillsConfigCmd)
	skillsCmd.AddCommand(skillsVersionsCmd)
	skillsCmd.AddCommand(skillsRollbackCmd)

	// Hub subcommands
	skillsHubCmd.AddCommand(skillsHubListCmd)
	skillsHubCmd.AddCommand(skillsHubAuditCmd)

	// Config subcommands
	skillsConfigCmd.AddCommand(skillsConfigListCmd)
	skillsConfigCmd.AddCommand(skillsConfigDisabledCmd)

	// Auto-skill lifecycle subcommands
	skillsCmd.AddCommand(skillsAutoCmd)
	skillsAutoCmd.AddCommand(skillsAutoListCmd)
	skillsAutoCmd.AddCommand(skillsAutoPendingCmd)
	skillsAutoCmd.AddCommand(skillsAutoApproveCmd)
	skillsAutoCmd.AddCommand(skillsAutoRejectCmd)
	skillsAutoCmd.AddCommand(skillsAutoArchiveCmd)
	skillsAutoCmd.AddCommand(skillsAutoRestoreCmd)
	skillsAutoCmd.AddCommand(skillsAutoDeleteCmd)
	skillsAutoCmd.AddCommand(skillsAutoStatusCmd)
	skillsAutoCmd.AddCommand(skillsAutoStatsCmd)

	// migrate command is added in skill_migrate.go
	// list command is added in skill_list.go

	skillsCreateCmd.Flags().BoolVarP(&skillCreateForce, "force", "f", false, "Overwrite if skill exists")
	skillsInstallCmd.Flags().StringVar(&skillInstallFrom, "from", "", "Source path or URL")

	rootCmd.AddCommand(skillsCmd)
}

func runSkillsShow(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}

	skill, err := mgr.Get(args[0])
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Name:        %s\n", skill.Name)
	fmt.Printf("Description: %s\n", skill.Description)
	fmt.Printf("Version:     %s\n", skill.Version)
	fmt.Printf("Author:      %s\n", skill.Author)
	fmt.Printf("Source:      %s\n", string(skill.Source))

	if len(skill.Tags) > 0 {
		fmt.Printf("Tags:        %s\n", strings.Join(skill.Tags, ", "))
	}
	if len(skill.Tools) > 0 {
		fmt.Printf("Tools:       %s\n", strings.Join(skill.Tools, ", "))
		if missing := unregisteredTools(skill.GetTools()); len(missing) > 0 {
			fmt.Printf("Unregistered: %s  (declared but not registered)\n", strings.Join(missing, ", "))
		}
	}

	fmt.Println("\n--- Content ---")
	fmt.Println(skill.Content)
}

// unregisteredTools 返回技能声明了、但当前工具注册表里不存在的工具名。
// 让 `magic skills show` 能直接暴露"技能声明与实况不符"，而不是静默放过。
func unregisteredTools(declared []string) []string {
	if len(declared) == 0 {
		return nil
	}
	reg := tool.NewRegistry()
	reg.RegisterAll("")
	known := make(map[string]struct{})
	for _, n := range reg.List() {
		known[n] = struct{}{}
	}
	var missing []string
	for _, t := range declared {
		if _, ok := known[t]; !ok {
			missing = append(missing, t)
		}
	}
	return missing
}

func runSkillsSearch(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}

	keyword := args[0]
	results := mgr.Search(keyword)

	if len(results) == 0 {
		fmt.Printf("No skills found matching '%s'\n", keyword)
		return
	}

	fmt.Printf("Found %d skills matching '%s':\n\n", len(results), keyword)
	for _, s := range results {
		tags := ""
		if len(s.Tags) > 0 {
			tags = fmt.Sprintf(" [%s]", strings.Join(s.Tags, ", "))
		}
		fmt.Printf("  • %s: %s%s\n", s.Name, s.Description, tags)
	}
}

func runSkillsInstall(cmd *cobra.Command, args []string) {
	name := args[0]
	from := skillInstallFrom

	// Check if already installed
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}

	if _, err := mgr.Get(name); err == nil {
		fmt.Printf("Skill '%s' is already installed.\n", name)
		fmt.Println("Use 'magic skills delete " + name + "' first to reinstall.")
		return
	}

	// Determine source and install
	if from != "" {
		// Install from local path or URL
		installSkillFromPath(name, from)
	} else {
		// 从官方 Hub registry 安装（InstallFromHub 仅支持 HubSourceHub）
		fmt.Printf("Installing skill '%s' from Hub registry...\n", name)

		err := mgr.InstallFromHub(skills.HubSourceHub, name)
		if err != nil {
			fmt.Printf("Failed to install from registry: %v\n", err)
			fmt.Println("\nSupported sources:")
			fmt.Println("  - GitHub: magic skills install my-skill --from github:owner/repo/path")
			fmt.Println("  - URL: magic skills install my-skill --from https://example.com/skill.zip")
			fmt.Println("  - skills.sh: magic skills install my-skill --from skills.sh/name")
			fmt.Println("  - Local: magic skills install my-skill --from /path/to/skill")
			os.Exit(1)
		}

		fmt.Println("\n✓ Skill installed successfully from registry!")
	}
}

func installSkillFromPath(name, path string) {
	// Check if it's a URL (remote source)
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") ||
		strings.HasPrefix(path, "github:") || strings.HasPrefix(path, "skills.sh/") {
		installSkillFromURL(name, path)
		return
	}

	// Check if source exists
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		fmt.Printf("Path not found: %s\n", path)
		os.Exit(1)
	}

	// Create skills directory
	skillsDir := filepath.Join(config.GetMagicHome(), "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		fmt.Printf("Failed to create skills directory: %v\n", err)
		os.Exit(1)
	}

	// Determine destination
	var dest string
	if info.IsDir() {
		dest = filepath.Join(skillsDir, name)
	} else {
		dest = filepath.Join(skillsDir, filepath.Base(path))
	}

	// Check if already exists
	if _, err := os.Stat(dest); err == nil {
		fmt.Printf("Skill '%s' already exists at: %s\n", name, dest)
		fmt.Println("Use --force to overwrite.")
		return
	}

	// Copy file or directory
	fmt.Printf("Installing skill '%s'...\n", name)
	fmt.Printf("  From: %s\n", path)
	fmt.Printf("  To:   %s\n", dest)

	if info.IsDir() {
		if err := copyDirMigrate(path, dest); err != nil {
			fmt.Printf("Failed to copy directory: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := copyFile(path, dest); err != nil {
			fmt.Printf("Failed to copy file: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Println("\n✓ Skill installed successfully!")
}

// installSkillFromURL 从远程 URL 安装技能
func installSkillFromURL(name, url string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Installing skill '%s' from remote source...\n", name)
	fmt.Printf("  URL: %s\n", url)

	// 解析 URL 类型并确定 HubSource
	var source skills.HubSource
	switch {
	case strings.HasPrefix(url, "github:"):
		source = skills.HubSourceGitHub
		url = strings.TrimPrefix(url, "github:")
		url = "https://github.com/" + url
	case strings.HasPrefix(url, "skills.sh/"):
		source = skills.HubSourceSkillsSh
	case strings.Contains(url, "well-known"):
		source = skills.HubSourceWellKnown
	default:
		source = skills.HubSourceHub
	}

	err = mgr.InstallFromHub(source, url)
	if err != nil {
		fmt.Printf("Failed to install from URL: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n✓ Skill installed successfully!")
}

func runSkillsCreate(cmd *cobra.Command, args []string) {
	name := args[0]

	// P1-2：走 Manager.Create 而不是自己 os.WriteFile 模板。
	// 自己写文件会绕过 Manager 的版本快照与技能注册 —— CLI 建出来的技能
	// 既没有版本历史（无法回滚），也不会出现在 skills list 里。
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}
	defer mgr.Close()

	if !skillCreateForce {
		if _, err := mgr.Get(name); err == nil {
			fmt.Printf("Skill '%s' already exists\n", name)
			fmt.Println("Use --force to overwrite.")
			return
		}
	} else if _, err := mgr.Get(name); err == nil {
		// --force：先删除再重建。删除前 Manager 会落一份内容快照，
		// 因此覆盖同样可回滚。
		if err := mgr.Remove(name); err != nil {
			fmt.Printf("Failed to overwrite existing skill: %v\n", err)
			os.Exit(1)
		}
	}

	content := fmt.Sprintf("# %s\n\n## Procedure\n<!-- 描述具体的操作步骤 -->\n\n## Pitfalls\n<!-- 已知的陷阱和注意事项 -->\n\n## Verification\n<!-- 如何验证操作成功 -->\n", name)
	skill, err := mgr.Create(name, "Describe what this skill does", content, []string{})
	if err != nil {
		fmt.Printf("Failed to create skill: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Created skill '%s' at %s\n", name, skill.Dir)
	fmt.Printf("  Edit %s to customize\n", filepath.Join(skill.Dir, "SKILL.md"))
}

func runSkillsDelete(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}

	err = mgr.Remove(args[0])
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Skill '%s' deleted.\n", args[0])
}

func runSkillsMatch(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}

	input := args[0]
	results := mgr.MatchSkillsByInput(input)

	if len(results) == 0 {
		fmt.Printf("No skills found matching '%s'\n", input)
		return
	}

	fmt.Printf("Found %d skills matching '%s':\n\n", len(results), input)
	for _, s := range results {
		fmt.Printf("  • %s: %s\n", s.Name, s.Description)
	}
}

// =============================================================================
// Hub, Disable/Enable Commands
// =============================================================================

func runSkillsUninstall(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}

	skillName := args[0]
	err = mgr.UninstallHubSkill(skillName)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Uninstalled hub skill '%s'\n", skillName)
}

func runSkillsDisable(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}

	skillName := args[0]
	err = mgr.DisableSkill(skillName)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Disabled skill '%s'\n", skillName)
	fmt.Println("  Use 'magic skills enable " + skillName + "' to re-enable.")
}

func runSkillsEnable(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}

	skillName := args[0]
	err = mgr.EnableSkill(skillName)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Enabled skill '%s'\n", skillName)
}

func runSkillsHubList(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}

	entries := mgr.GetHubLockEntries()
	if len(entries) == 0 {
		fmt.Println("No hub skills installed.")
		return
	}

	fmt.Printf("Hub-installed skills (%d):\n\n", len(entries))
	for _, e := range entries {
		fmt.Printf("  📦 %s\n", e.SkillName)
		fmt.Printf("     Source: %s\n", e.Source)
		fmt.Printf("     Installed: %s\n", e.InstalledAt.Format("2006-01-02 15:04"))
		if e.SecurityAudit != "" {
			fmt.Printf("     Security: %s\n", e.SecurityAudit)
		}
		fmt.Println()
	}
}

func runSkillsHubAudit(cmd *cobra.Command, args []string) {
	// 读取审计日志
	auditLog := filepath.Join(config.GetMagicHome(), "skills", ".hub", "audit.log")

	data, err := os.ReadFile(auditLog)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No audit log found.")
		} else {
			fmt.Printf("Error reading audit log: %v\n", err)
		}
		return
	}

	fmt.Print(string(data))
}

func runSkillsConfigList(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}

	disabled := mgr.GetDisabledSkills()

	fmt.Println("Skill Configuration:")
	fmt.Println()

	fmt.Println("Disabled Skills (Global):")
	if len(disabled.Global) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, name := range disabled.Global {
			fmt.Printf("  • %s\n", name)
		}
	}
	fmt.Println()

	fmt.Println("Disabled Skills (By Platform):")
	if len(disabled.Platform) == 0 {
		fmt.Println("  (none)")
	} else {
		for platform, names := range disabled.Platform {
			fmt.Printf("  %s:\n", platform)
			for _, name := range names {
				fmt.Printf("    • %s\n", name)
			}
		}
	}
}

func runSkillsConfigDisabled(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}

	disabled := mgr.GetDisabledSkills()

	fmt.Println("Disabled Skills:")
	fmt.Println()

	fmt.Println("Global:")
	if len(disabled.Global) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, name := range disabled.Global {
			fmt.Printf("  • %s\n", name)
		}
	}
	fmt.Println()

	fmt.Println("By Platform:")
	if len(disabled.Platform) == 0 {
		fmt.Println("  (none)")
	} else {
		for platform, names := range disabled.Platform {
			fmt.Printf("  %s:\n", platform)
			for _, name := range names {
				fmt.Printf("    • %s\n", name)
			}
		}
	}
}

// =============================================================================
// Auto-Skill Lifecycle Command Handlers
// =============================================================================

func printAutoSkillTable(title string, autoSkills []*skills.Skill) {
	fmt.Printf("%s (%d):\n", title, len(autoSkills))
	if len(autoSkills) == 0 {
		fmt.Println("  (none)")
		return
	}
	for _, s := range autoSkills {
		status := s.Status
		if status == "" {
			status = skills.SkillStatusPending
		}
		tags := strings.Join(s.Tags, ", ")
		fmt.Printf("  • %s\n", s.Name)
		fmt.Printf("      Description: %s\n", s.Description)
		fmt.Printf("      Status:      %s\n", status)
		fmt.Printf("      Directory:   %s\n", s.Dir)
		if tags != "" {
			fmt.Printf("      Tags:        %s\n", tags)
		}
		fmt.Println()
	}
}

func runSkillsAutoList(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}

	pending := mgr.ListAutoSkillsByStatus(skills.SkillStatusPending)
	approved := mgr.ListAutoSkillsByStatus(skills.SkillStatusApproved)
	archived := mgr.ListAutoSkillsByStatus(skills.SkillStatusArchived)
	rejected := mgr.ListAutoSkillsByStatus(skills.SkillStatusRejected)

	total := len(pending) + len(approved) + len(archived) + len(rejected)
	fmt.Printf("Auto-Generated Skills (%d total)\n", total)
	fmt.Println()

	printAutoSkillTable("  Pending (awaiting review)", pending)
	fmt.Println()
	printAutoSkillTable("  Approved (active, used by agent)", approved)
	fmt.Println()
	printAutoSkillTable("  Archived (kept, not active)", archived)
	fmt.Println()
	printAutoSkillTable("  Rejected", rejected)
}

func runSkillsAutoPending(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}
	pending := mgr.ListAutoSkillsByStatus(skills.SkillStatusPending)
	printAutoSkillTable("Pending skills", pending)
}

func runSkillsAutoApprove(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}
	name := args[0]
	if err := mgr.ApproveAutoSkill(name); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Skill '%s' has been approved and will now be used by the agent.\n", name)
}

func runSkillsAutoReject(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}
	name := args[0]
	if err := mgr.RejectAutoSkill(name); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Skill '%s' has been rejected.\n", name)
}

func runSkillsAutoArchive(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}
	name := args[0]
	if err := mgr.ArchiveAutoSkill(name); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Skill '%s' has been archived.\n", name)
}

func runSkillsAutoRestore(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}
	name := args[0]
	if err := mgr.RestoreAutoSkill(name); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Skill '%s' has been restored from archive to approved.\n", name)
}

func runSkillsAutoDelete(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}
	name := args[0]
	if err := mgr.DeleteAutoSkill(name); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Skill '%s' has been permanently deleted.\n", name)
}

func runSkillsAutoStatus(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}
	name := args[0]
	status, err := mgr.GetSkillStatus(name)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Skill '%s' status: %s\n", name, status)
}

func runSkillsAutoStats(cmd *cobra.Command, args []string) {
	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}
	counts := mgr.GetSkillStatusCounts()
	fmt.Println("Auto-Generated Skill Status Counts:")
	fmt.Println()
	total := 0
	for status, count := range counts {
		fmt.Printf("  %-10s: %d\n", status, count)
		total += count
	}
	fmt.Println("  ----------: ----")
	fmt.Printf("  %-10s: %d\n", "Total", total)
}

// runSkillsVersions 列出技能的版本历史（P1-2）。
func runSkillsVersions(cmd *cobra.Command, args []string) {
	name := args[0]

	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}
	defer mgr.Close()

	versions := mgr.GetVersions(name)
	if len(versions) == 0 {
		fmt.Printf("No version history for skill '%s'.\n", name)
		fmt.Println("(Versions are recorded when a skill is created, updated or deleted.)")
		return
	}

	fmt.Printf("Version history for skill '%s' (%d version(s)):\n\n", name, len(versions))
	for _, v := range versions {
		marker := " "
		if v.IsCurrent {
			marker = "*"
		}
		fmt.Printf("%s %-8s  %-20s  quality=%.0f  by=%s\n",
			marker, v.Version, v.CreatedAt.Format("2006-01-02 15:04:05"), v.QualityScore, v.CreatedBy)
		if len(v.Changes) > 0 {
			fmt.Printf("    changes: %s\n", strings.Join(v.Changes, ", "))
		}
	}
	fmt.Println()
	fmt.Println("(* = current version; use `magic skills rollback <name> <version>` to revert)")
}

// runSkillsRollback 把技能回滚到指定版本（P1-2）。
func runSkillsRollback(cmd *cobra.Command, args []string) {
	name, version := args[0], args[1]

	mgr, err := skills.NewManager()
	if err != nil {
		fmt.Printf("Failed to load skills: %v\n", err)
		os.Exit(1)
	}
	defer mgr.Close()

	if err := mgr.RollbackSkillVersion(name, version); err != nil {
		fmt.Printf("Failed to rollback skill '%s' to %s: %v\n", name, version, err)
		os.Exit(1)
	}

	fmt.Printf("Rolled back skill '%s' to version %s.\n", name, version)
	fmt.Println("Restart `magic gateway` / `magic chat` for the change to take effect in memory.")
}
