"use client";

import * as React from "react";

import {
  CheckCircle2Icon,
  DownloadIcon,
  FileDiffIcon,
  LoaderCircleIcon,
  PlayIcon,
  RefreshCwIcon,
  ShieldCheckIcon,
  ShieldQuestionIcon,
  XCircleIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { DefenseBundle, DefenseTask, DefenseTaskListItem } from "@/lib/defense";

function paths(value: string) {
  return value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function newTask(
  repositoryRoot: string,
  readPaths: string,
  writePaths: string,
  image: string,
  network: boolean,
): DefenseTask {
  return {
    schema_version: "defense.artex/v1",
    id: `defense-${Date.now()}`,
    profile: "defense",
    repository_root: repositoryRoot.trim(),
    default_branch: "main",
    scope: ["local-repository"],
    constraints: ["read-only source audit", "diff-only patch", "no commits or network writes", "human review required"],
    allowed_read_paths: paths(readPaths),
    allowed_write_paths: paths(writePaths),
    limits: {
      max_file_bytes: 1048576,
      max_content_bytes: 131072,
      max_search_results: 200,
      max_files_changed: 5,
      max_changed_lines: 250,
      timeout_seconds: 120,
      memory_mb: 1024,
      cpu_millicpus: 1000,
      pids: 128,
      output_bytes: 65536,
    },
    validation: {
      image: image.trim(),
      network_allowed: network,
      require_baseline_failure: true,
      command_allowlist: ["go", "node", "npm", "govulncheck"],
    },
    status: "draft",
    created_at: new Date().toISOString(),
  };
}

function StatusBadge({ value }: { value?: string }) {
  if (!value) return null;
  const destructive = value === "failed" || value === "rejected";
  const positive = value === "passed" || value === "validated" || value === "accepted" || value === "completed";
  let variant: "default" | "destructive" | "outline" = "outline";
  if (destructive) variant = "destructive";
  else if (positive) variant = "default";
  return <Badge variant={variant}>{value.replaceAll("_", " ")}</Badge>;
}

function commandBadgeVariant(command: { exit_code: number; kind: string }): "default" | "destructive" | "outline" {
  if (command.exit_code === 0) return "outline";
  if (command.kind === "baseline_regression") return "default";
  return "destructive";
}

function EmptyState({ text }: { text: string }) {
  return (
    <div className="text-muted-foreground flex min-h-40 items-center justify-center rounded-lg border border-dashed p-6 text-center text-sm">
      {text}
    </div>
  );
}

export default function DefensePage() {
  const [repositoryRoot, setRepositoryRoot] = React.useState("");
  const [readPaths, setReadPaths] = React.useState(".");
  const [writePaths, setWritePaths] = React.useState("merge.js\nmerge.regression.test.js");
  const [image, setImage] = React.useState("node:22-bookworm");
  const [network, setNetwork] = React.useState(false);
  const [tasks, setTasks] = React.useState<DefenseTaskListItem[]>([]);
  const [selected, setSelected] = React.useState<DefenseBundle | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [busy, setBusy] = React.useState<"audit" | "run" | "approve" | null>(null);
  const [approvalOpen, setApprovalOpen] = React.useState(false);
  const [tab, setTab] = React.useState("findings");

  const loadTasks = React.useCallback(async () => {
    setLoading(true);
    try {
      setTasks((await api.defenseTasks()) ?? []);
    } catch (error) {
      toast.error((error as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void loadTasks();
  }, [loadTasks]);

  const selectTask = async (id: string) => {
    try {
      setSelected(await api.defenseTask(id));
      setTab("findings");
    } catch (error) {
      toast.error((error as Error).message);
    }
  };

  const create = async (mode: "audit" | "run") => {
    if (!repositoryRoot.trim()) {
      toast.error("Enter an absolute local repository path.");
      return;
    }
    setBusy(mode);
    try {
      const bundle = await api.createDefenseTask(newTask(repositoryRoot, readPaths, writePaths, image, network), mode);
      setSelected(bundle);
      setTab(mode === "run" && bundle.patch ? "patch" : "findings");
      toast.success(
        mode === "audit" ? "Read-only audit complete." : "Defense run complete. Review the recorded evidence.",
      );
      await loadTasks();
    } catch (error) {
      toast.error((error as Error).message);
      await loadTasks();
    } finally {
      setBusy(null);
    }
  };

  const approve = async () => {
    if (!selected) return;
    setBusy("approve");
    try {
      setSelected(await api.approveDefenseTask(selected.task.id));
      setApprovalOpen(false);
      toast.success("Human approval recorded. No commit or push was performed.");
      await loadTasks();
    } catch (error) {
      toast.error((error as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const download = async (
    file: "bundle.json" | "patch.diff" | "remediation.md" | "validation.json" | "findings.json",
  ) => {
    if (!selected) return;
    try {
      await api.downloadDefenseArtifact(selected.task.id, file);
    } catch (error) {
      toast.error((error as Error).message);
    }
  };

  const findings = selected?.assessment.findings ?? [];
  const scans = selected?.assessment.scans ?? [];
  const patch = selected?.patch;
  const validation = selected?.validation;

  return (
    <div className="flex flex-1 flex-col">
      <header className="bg-background/95 sticky top-0 z-10 flex flex-wrap items-center gap-3 border-b px-4 py-3 backdrop-blur lg:px-6">
        <SidebarTrigger className="-ml-1" />
        <ShieldCheckIcon className="text-primary size-5" />
        <div>
          <h1 className="text-sm font-semibold">ARTEX Defense</h1>
          <p className="text-muted-foreground text-xs">Finding in → evidence-backed patch and validation out</p>
        </div>
        <Badge variant="outline" className="ml-auto">
          default deny
        </Badge>
      </header>

      <main className="grid flex-1 gap-4 p-4 lg:grid-cols-[360px_minmax(0,1fr)] lg:p-6">
        <div className="flex flex-col gap-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-sm">New local-repository task</CardTitle>
              <CardDescription>No source edits, shell tool, commits, pushes, merges, or deployment.</CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-4">
              <label htmlFor="defense-repository" className="flex flex-col gap-1.5 text-xs font-medium">
                Repository root
                <Input
                  id="defense-repository"
                  value={repositoryRoot}
                  onChange={(event) => setRepositoryRoot(event.target.value)}
                  placeholder="/absolute/path/to/repository"
                />
              </label>
              <label htmlFor="defense-read-paths" className="flex flex-col gap-1.5 text-xs font-medium">
                Allowed read paths
                <Textarea
                  id="defense-read-paths"
                  value={readPaths}
                  onChange={(event) => setReadPaths(event.target.value)}
                  rows={2}
                  className="font-mono text-xs"
                />
              </label>
              <label htmlFor="defense-write-paths" className="flex flex-col gap-1.5 text-xs font-medium">
                Allowed patch paths
                <Textarea
                  id="defense-write-paths"
                  value={writePaths}
                  onChange={(event) => setWritePaths(event.target.value)}
                  rows={3}
                  className="font-mono text-xs"
                />
              </label>
              <label htmlFor="defense-image" className="flex flex-col gap-1.5 text-xs font-medium">
                Local validation image
                <Input
                  id="defense-image"
                  value={image}
                  onChange={(event) => setImage(event.target.value)}
                  className="font-mono text-xs"
                />
              </label>
              <div className="flex items-start justify-between gap-3 rounded-md border p-3">
                <div className="text-xs">
                  <div className="font-medium">Allow dependency-scan network access</div>
                  <div className="text-muted-foreground mt-1">
                    Off by default. Validation containers use --network none when off.
                  </div>
                </div>
                <Switch checked={network} onCheckedChange={setNetwork} aria-label="Allow network access" />
              </div>
              <div className="grid grid-cols-2 gap-2">
                <Button variant="outline" disabled={busy !== null} onClick={() => void create("audit")}>
                  {busy === "audit" ? <LoaderCircleIcon className="animate-spin" /> : <ShieldQuestionIcon />}
                  Audit only
                </Button>
                <Button disabled={busy !== null} onClick={() => void create("run")}>
                  {busy === "run" ? <LoaderCircleIcon className="animate-spin" /> : <PlayIcon />}
                  Patch & validate
                </Button>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader className="flex-row items-center justify-between">
              <CardTitle className="text-sm">Review sessions</CardTitle>
              <Button
                size="icon"
                variant="ghost"
                className="size-7"
                onClick={() => void loadTasks()}
                disabled={loading}
              >
                <RefreshCwIcon className={loading ? "animate-spin" : ""} />
              </Button>
            </CardHeader>
            <CardContent className="flex flex-col gap-2">
              {tasks.length === 0 && <p className="text-muted-foreground text-xs">No defense sessions yet.</p>}
              {tasks.map((task) => (
                <button
                  key={task.id}
                  type="button"
                  onClick={() => void selectTask(task.id)}
                  className={`hover:bg-muted rounded-md border p-3 text-left ${selected?.task.id === task.id ? "border-primary" : ""}`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="truncate text-xs font-medium">{task.id}</span>
                    <StatusBadge value={task.status} />
                  </div>
                  <div className="text-muted-foreground mt-1 truncate font-mono text-[11px]">
                    {task.repository_root}
                  </div>
                  <div className="text-muted-foreground mt-2 text-[11px]">
                    {task.finding_count} findings · {task.validation_status ?? "not validated"}
                  </div>
                </button>
              ))}
            </CardContent>
          </Card>
        </div>

        <div className="min-w-0">
          {!selected ? (
            <EmptyState text="Create a local audit or select a review session. Uncertain findings remain needs_review and never receive invented patches." />
          ) : (
            <div className="flex flex-col gap-4">
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="mr-2 text-sm font-semibold">{selected.task.id}</h2>
                <StatusBadge value={selected.task.status} />
                {patch && <StatusBadge value={patch.status} />}
                {validation && <StatusBadge value={validation.status} />}
                <div className="ml-auto flex flex-wrap gap-2">
                  <Button size="sm" variant="outline" onClick={() => void download("bundle.json")}>
                    <DownloadIcon />
                    Bundle
                  </Button>
                  {patch && (
                    <Button size="sm" variant="outline" onClick={() => void download("patch.diff")}>
                      <FileDiffIcon />
                      Diff
                    </Button>
                  )}
                  <Button size="sm" variant="outline" onClick={() => void download("remediation.md")}>
                    <DownloadIcon />
                    Report
                  </Button>
                  {patch && validation?.status === "passed" && patch.status === "draft" && (
                    <Button size="sm" onClick={() => setApprovalOpen(true)}>
                      <CheckCircle2Icon />
                      Human approval
                    </Button>
                  )}
                </div>
              </div>

              <Alert>
                <ShieldCheckIcon />
                <AlertTitle>Evidence is separate from approval</AlertTitle>
                <AlertDescription>
                  Validation is based on actual command output. A passing run does not commit, push, merge, deploy, or
                  approve its own patch.
                </AlertDescription>
              </Alert>

              <Tabs value={tab} onValueChange={setTab}>
                <TabsList className="flex-wrap">
                  <TabsTrigger value="findings">Findings ({findings.length})</TabsTrigger>
                  <TabsTrigger value="patch" disabled={!patch}>
                    Patch diff
                  </TabsTrigger>
                  <TabsTrigger value="validation" disabled={!validation}>
                    Validation
                  </TabsTrigger>
                  <TabsTrigger value="report">Review report</TabsTrigger>
                </TabsList>

                <TabsContent value="findings" className="mt-4 flex flex-col gap-3">
                  {findings.length === 0 && (
                    <EmptyState text="No findings. Scanner records below document what was checked." />
                  )}
                  {findings.map((finding) => (
                    <Card key={finding.id}>
                      <CardHeader>
                        <div className="flex flex-wrap items-start justify-between gap-2">
                          <div>
                            <CardTitle className="text-sm">{finding.title}</CardTitle>
                            <CardDescription className="mt-1 font-mono text-[11px]">
                              {finding.rule_id} · {finding.source_scanner} {finding.scanner_version}
                            </CardDescription>
                          </div>
                          <div className="flex gap-2">
                            <StatusBadge value={finding.severity} />
                            <StatusBadge value={finding.status} />
                          </div>
                        </div>
                      </CardHeader>
                      <CardContent className="flex flex-col gap-3 text-xs">
                        <div className="grid gap-2 md:grid-cols-3">
                          <div>
                            <span className="text-muted-foreground">Confidence</span>
                            <div>{finding.confidence}</div>
                          </div>
                          <div>
                            <span className="text-muted-foreground">Reachability</span>
                            <div>{finding.reachability}</div>
                          </div>
                          <div>
                            <span className="text-muted-foreground">Patchable</span>
                            <div>{finding.patchable ? "yes" : "no / manual review"}</div>
                          </div>
                        </div>
                        <div>
                          <span className="text-muted-foreground">Source → sink</span>
                          <div className="mt-1 font-mono">
                            {finding.source} → {finding.sink}
                          </div>
                        </div>
                        {finding.rejection_reason && (
                          <Alert variant="destructive">
                            <XCircleIcon />
                            <AlertDescription>{finding.rejection_reason}</AlertDescription>
                          </Alert>
                        )}
                        {finding.evidence.map((evidence) => (
                          <div
                            key={`${finding.id}-${evidence.path}-${evidence.start_line}-${evidence.kind}`}
                            className="rounded-md border p-3"
                          >
                            <div className="font-mono text-[11px]">
                              {evidence.path}:{evidence.start_line || 0} · {evidence.kind}
                            </div>
                            <p className="mt-2 leading-relaxed">{evidence.description}</p>
                            {evidence.snippet && (
                              <pre className="bg-muted mt-2 overflow-auto rounded p-2 font-mono text-[11px] whitespace-pre-wrap">
                                {evidence.snippet}
                              </pre>
                            )}
                          </div>
                        ))}
                        <div className="text-muted-foreground font-mono text-[10px] break-all">
                          {finding.fingerprint}
                        </div>
                      </CardContent>
                    </Card>
                  ))}
                  <Card>
                    <CardHeader>
                      <CardTitle className="text-sm">Scanner records</CardTitle>
                    </CardHeader>
                    <CardContent className="flex flex-col gap-2 text-xs">
                      {scans.map((scan) => (
                        <div key={`${scan.scanner}-${scan.command.join(" ")}`} className="rounded border p-2">
                          <span className="font-medium">{scan.scanner}</span> · {scan.finding_count} findings
                          <div className="text-muted-foreground font-mono text-[11px]">{scan.command.join(" ")}</div>
                          {scan.warnings?.map((warning) => (
                            <div key={warning} className="mt-1 text-amber-600">
                              {warning}
                            </div>
                          ))}
                        </div>
                      ))}
                    </CardContent>
                  </Card>
                </TabsContent>

                <TabsContent value="patch" className="mt-4">
                  {patch ? (
                    <Card>
                      <CardHeader>
                        <CardTitle className="text-sm">{patch.summary}</CardTitle>
                        <CardDescription>
                          {patch.rationale} Risk: {patch.risk_level}. Reviewer: {patch.reviewed_by ?? "not reviewed"}.
                        </CardDescription>
                      </CardHeader>
                      <CardContent>
                        <pre className="bg-muted max-h-[65vh] overflow-auto rounded-md p-4 font-mono text-xs leading-5 whitespace-pre-wrap">
                          {patch.unified_diff}
                        </pre>
                      </CardContent>
                    </Card>
                  ) : (
                    <EmptyState text="No patch was generated. Review the finding state and rejection reason." />
                  )}
                </TabsContent>

                <TabsContent value="validation" className="mt-4">
                  {validation ? (
                    <div className="flex flex-col gap-4">
                      <Card>
                        <CardHeader>
                          <CardTitle className="text-sm">Independent validation</CardTitle>
                          <CardDescription className="font-mono text-[11px] break-all">
                            {validation.environment_digest}
                          </CardDescription>
                        </CardHeader>
                        <CardContent>
                          <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
                            {[
                              ["Baseline must fail", validation.baseline_test_result.status],
                              ["Patched tests", validation.patched_test_result.status],
                              ["Build", validation.build_result.status],
                              ["Scanner", validation.scanner_result.status],
                            ].map(([name, status]) => (
                              <div key={name} className="rounded border p-3">
                                <div className="text-muted-foreground text-xs">{name}</div>
                                <div className="mt-1">
                                  <StatusBadge value={status} />
                                </div>
                              </div>
                            ))}
                          </div>
                          {validation.failure_reason && (
                            <Alert variant="destructive" className="mt-4">
                              <XCircleIcon />
                              <AlertDescription>{validation.failure_reason}</AlertDescription>
                            </Alert>
                          )}
                        </CardContent>
                      </Card>
                      {validation.commands.map((command) => (
                        <Card key={`${command.started_at}-${command.kind}-${command.command.join(" ")}`}>
                          <CardHeader>
                            <div className="flex flex-wrap items-center justify-between gap-2">
                              <CardTitle className="font-mono text-xs">{command.command.join(" ")}</CardTitle>
                              <Badge variant={commandBadgeVariant(command)}>exit {command.exit_code}</Badge>
                            </div>
                            <CardDescription>
                              {command.kind} · {(command.duration / 1000000).toFixed(0)} ms · {command.environment}
                            </CardDescription>
                          </CardHeader>
                          <CardContent>
                            <pre className="bg-muted max-h-80 overflow-auto rounded p-3 font-mono text-xs whitespace-pre-wrap">
                              {command.output || "(no output)"}
                            </pre>
                            {command.output_truncated && (
                              <p className="mt-2 text-xs text-amber-600">Output was capped by policy.</p>
                            )}
                          </CardContent>
                        </Card>
                      ))}
                    </div>
                  ) : (
                    <EmptyState text="No validation run exists." />
                  )}
                </TabsContent>

                <TabsContent value="report" className="mt-4">
                  <Card>
                    <CardHeader>
                      <CardTitle className="text-sm">PR-ready remediation report</CardTitle>
                      <CardDescription>
                        Evidence, assumptions, unresolved questions, residual risk, and rollback instructions.
                      </CardDescription>
                    </CardHeader>
                    <CardContent>
                      <pre className="bg-muted max-h-[70vh] overflow-auto rounded p-4 font-mono text-xs whitespace-pre-wrap">
                        {selected.report}
                      </pre>
                    </CardContent>
                  </Card>
                </TabsContent>
              </Tabs>
            </div>
          )}
        </div>
      </main>

      <Dialog open={approvalOpen} onOpenChange={setApprovalOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Record independent human approval?</DialogTitle>
            <DialogDescription>
              Confirm that you reviewed the diff, baseline failure, patched tests, build, scanner output, residual risk,
              and rollback notes. This records review only; it does not commit or push.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setApprovalOpen(false)}>
              Cancel
            </Button>
            <Button disabled={busy === "approve"} onClick={() => void approve()}>
              {busy === "approve" ? <LoaderCircleIcon className="animate-spin" /> : <CheckCircle2Icon />}Confirm human
              review
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
