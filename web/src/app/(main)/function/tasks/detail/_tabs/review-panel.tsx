// ReviewPanel combines a single task's review conversation, pending intents, intercept confirmations, and auto-disposition settings.
/**
 * Main functions/types:
  *   - ReviewPanel: main review panel component (messages, pending-intent list, approve/reject actions).
  *   - prefillRejectReason / fmtTime: reject-reason prefill and time-formatting helpers.
 */
"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import {
	CheckIcon,
	XIcon,
	ArrowUpIcon,
	TriangleAlertIcon,
	Loader2Icon,
} from "lucide-react";
import { api } from "@/lib/api";
import type { InterceptPending, ReviewMessage, ReviewProposedIntent as ProposedIntent } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { InputGroup, InputGroupAddon, InputGroupButton, InputGroupTextarea } from "@/components/ui/input-group";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
	AlertDialog,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alert-dialog";

// ── Types ──────────────────────────────────────────────

// prefillRejectReason turns the AI pre-screen opinion into an editable reject-reason draft.
function prefillRejectReason(it: ProposedIntent | null): string {
	return (it?.review_note ?? "").trim();
}

// System events (pre-screen records, approve/reject notices, etc.) render as centered capsules, not chat bubbles.
const SYS_RE = /^(Pre-screen|Intent #|\[System\]|\u9884\u5ba1|\u610f\u56fe #|\u3010\u7cfb\u7edf\u3011)/;

function fmtTime(unixSec: number): string {
	const d = new Date(unixSec * 1000);
	return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString();
}

// ── Main component ────────────────────────────────────────────

export function ReviewPanel({ taskId }: { taskId: string }) {
	const [messages, setMessages] = useState<ReviewMessage[]>([]);
	const [proposed, setProposed] = useState<ProposedIntent[]>([]);
	const [selectedProposedID, setSelectedProposedID] = useState<number | null>(null);
	const [pending, setPending] = useState<InterceptPending[]>([]);
	const [pendingHasMore, setPendingHasMore] = useState(false);
	const [input, setInput] = useState("");
	const [sending, setSending] = useState(false);
		// Reject dialog
	const [rejectTarget, setRejectTarget] = useState<ProposedIntent | null>(null);
	const [rejectReason, setRejectReason] = useState("");
	const bottomRef = useRef<HTMLDivElement>(null);
		// Auto-scroll: only follows new messages when the user is already at the bottom; never yanks back while reading history.
	const scrollAreaRef = useRef<HTMLDivElement>(null);
	const stickBottomRef = useRef(true);
		// Uses message ids instead of the client clock to detect replies, avoiding browser/server clock drift.
	const awaitingRef = useRef(false);
	const awaitAfterIdRef = useRef(0);
	const messageRefreshTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
	const [awaiting, setAwaiting] = useState(false);
	const [awaitSlow, setAwaitSlow] = useState(false);
	const taskIdRef = useRef(taskId);
	taskIdRef.current = taskId;

	useEffect(() => {
		const vp = scrollAreaRef.current?.querySelector<HTMLElement>('[data-slot="scroll-area-viewport"]');
		if (!vp) return;
		const onScroll = () => {
			stickBottomRef.current = vp.scrollHeight - vp.scrollTop - vp.clientHeight < 60;
		};
		vp.addEventListener("scroll", onScroll, { passive: true });
		return () => vp.removeEventListener("scroll", onScroll);
	}, []);

		// The executor is decided by the backend at submit/revise time; the panel only displays it.

		// Clear the previous task's messages, drafts, and wait watermark on task switch to prevent cross-task contamination.
	useEffect(() => {
		setMessages([]);
		setProposed([]);
		setSelectedProposedID(null);
		setPending([]);
		setPendingHasMore(false);
		setInput("");
		setSending(false);
		awaitingRef.current = false;
		awaitAfterIdRef.current = 0;
		setAwaiting(false);
		setAwaitSlow(false);
	}, [taskId]);

		// Polling: message stream (3s) + pending list (5s) + intercept confirmation cards (2s)
	const loadMessages = useCallback(() => {
		const requestTaskId = taskId;
		api.reviewMessages(requestTaskId)
			.then((list) => {
				if (taskIdRef.current !== requestTaskId) return;
				setMessages(list);
								// Only terminal messages end the wait; review-process logs must not dismiss the final-reply indicator early.
				const terminalKinds = ["result", "error", "auto_result", "review_rejection"];
				if (awaitingRef.current && list.some((m) => m.id > awaitAfterIdRef.current && terminalKinds.includes(m.kind))) {
					awaitingRef.current = false;
					setAwaiting(false);
					setAwaitSlow(false);
				}
			})
			.catch(() => {});
	}, [taskId]);
	const loadProposed = useCallback(() => {
				// The backend filters pending intents by the current task and the review executor.
		const requestTaskId = taskId;
		api.reviewProposed(requestTaskId)
			.then((list) => { if (taskIdRef.current === requestTaskId) setProposed(list); })
			.catch(() => {});
	}, [taskId]);
	const loadPending = useCallback(() => {
				// Only the current task's first pending page is queried, avoiding downloading full approval history or the global queue.
		const requestTaskId = taskId;
		api.interceptTaskPage(requestTaskId, 1, 100, { status: "pending" })
			.then((page) => {
				if (taskIdRef.current !== requestTaskId) return;
				setPending(page.items.filter((p) => p.agent_name === "review"));
				setPendingHasMore(page.total > page.items.length);
			})
			.catch(() => {});
	}, [taskId]);

	useEffect(() => {
		loadMessages();
		loadProposed();
		loadPending();
		const t1 = setInterval(loadMessages, 3000);
		const t2 = setInterval(loadProposed, 5000);
		const t3 = setInterval(loadPending, 2000);
		return () => {
			clearInterval(t1);
			clearInterval(t2);
			clearInterval(t3);
			if (messageRefreshTimerRef.current) clearTimeout(messageRefreshTimerRef.current);
			awaitingRef.current = false;
		};
	}, [loadMessages, loadProposed, loadPending]);

		// Thinking timeout degradation: >45s prompts checking LLM connectivity; >120s removes the indicator (errors appear as red cards in the stream).
	useEffect(() => {
		if (!awaiting) return;
		const slowT = setTimeout(() => setAwaitSlow(true), 45000);
		const giveUpT = setTimeout(() => { awaitingRef.current = false; setAwaiting(false); setAwaitSlow(false); }, 120000);
		return () => { clearTimeout(slowT); clearTimeout(giveUpT); };
	}, [awaiting]);

	useEffect(() => {
		if (stickBottomRef.current) bottomRef.current?.scrollIntoView({ block: "end" });
	}, [messages, proposed, awaiting]);

		// Disposition actions
	const approve = async (it: ProposedIntent) => {
		try {
						// content_version is sent back for CAS; the backend resolves the executor from pending_executor.
			await api.reviewApprove(taskId, it.id, it.content_version);
			setProposed((l) => l.filter((x) => x.id !== it.id));
		} catch (e) {
			toast.error("Approve failed: " + (e as Error).message);
		}
	};
	const doReject = async () => {
		if (!rejectTarget || !rejectReason.trim()) return;
		try {
			await api.reviewReject(taskId, rejectTarget.id, rejectReason.trim(), rejectTarget.content_version);
			setProposed((l) => l.filter((x) => x.id !== rejectTarget.id));
			setRejectTarget(null);
			setRejectReason("");
		} catch (e) {
			toast.error("Reject failed: " + (e as Error).message);
		}
	};
		// Auto-disposition toggle; the backend ReviewRunner reads the latest setting on its next review round.
	const [reviewAuto, setReviewAuto] = useState(false);
	const [autoToggling, setAutoToggling] = useState(false);
		// Full manual review: when review is disabled the panel shows an empty state (the tab stays mounted rather than being hidden by the toggle).
	const [taskReviewEnabled, setTaskReviewEnabled] = useState<boolean | null>(null);
	useEffect(() => {
		api.task(taskId).then((t) => { setReviewAuto(!!t.review_auto); setTaskReviewEnabled(!!t.review_enabled); }).catch(() => {});
	}, [taskId]);
	const toggleAuto = async (v: boolean) => {
		if (autoToggling) return;
		setAutoToggling(true);
		try {
			await api.updateTask(taskId, { review_auto: v });
			setReviewAuto(v);
		} catch (e) {
			toast.error("Failed to update auto-disposition: " + (e as Error).message);
		} finally {
			setAutoToggling(false);
		}
	};

	const send = async () => {
		const v = input.trim();
		if (!v || sending) return;
		const requestTaskId = taskId;
		setSending(true);
		try {
						// Capture the task's message watermark before sending, so a send before the first load can't mistake a historical terminal message for a new reply.
			const current = await api.reviewMessages(requestTaskId);
			if (taskIdRef.current !== requestTaskId) return;
			setInput("");
			setMessages(current);
			stickBottomRef.current = true; // force-scroll to the bottom after sending my own message
			awaitAfterIdRef.current = current.reduce((max, m) => Math.max(max, m.id), 0);
			awaitingRef.current = true;
			setAwaiting(true);
			setAwaitSlow(false);
			await api.reviewChat(requestTaskId, v); // 202 accepted; the reply arrives via message-stream polling
		} catch (e) {
			if (taskIdRef.current === requestTaskId) {
				awaitingRef.current = false;
				setAwaiting(false);
				setAwaitSlow(false);
				setInput(v);
			}
			toast.error("Send failed: " + (e as Error).message);
		} finally {
			if (taskIdRef.current === requestTaskId) {
				setSending(false);
				messageRefreshTimerRef.current = setTimeout(loadMessages, 800);
			}
		}
	};
	const decidePending = async (p: InterceptPending, decision: "allowed" | "denied") => {
		try {
			await api.interceptDecide(p.id, decision);
			setPending((l) => l.filter((x) => x.id !== p.id));
		} catch (e) {
			toast.error("Confirmation failed: " + (e as Error).message);
		}
	};

	const selectedProposed = proposed.find((it) => it.id === selectedProposedID) ?? proposed[0] ?? null;
	const selectedProposedIndex = selectedProposed ? proposed.findIndex((it) => it.id === selectedProposed.id) : -1;

		// Tasks with review disabled: permanent empty state (all hooks have run, so this early return is safe).
	if (taskReviewEnabled === false) {
		return (
				<div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 text-sm text-muted-foreground">
				<p>Intent review is not enabled for this task</p>
				<p className="text-xs opacity-70">Once enabled, every intent requires manual approve/reject; turning on "auto-disposition" lets the review agent decide on its own</p>
			</div>
		);
	}

	return (
		<div className="flex min-h-0 flex-1 flex-col">
			          {/* ── Toolbar: pending count/mode note + auto-disposition toggle (plain style consistent with other session panes, no icons) ── */}
			<div className="flex items-center justify-between gap-3 border-b px-4 py-2">
				<div className="flex min-w-0 flex-wrap items-center gap-2 text-xs text-muted-foreground">
					{proposed.length > 0 && (
						<>
							<Badge variant="outline" className="border-amber-300 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-400">
								Pending: {proposed.length}
							</Badge>
							<div className="flex flex-wrap items-center gap-1" role="group" aria-label="Select pending intent">
								{proposed.map((it, index) => (
									<Button
										key={it.id}
										type="button"
										size="sm"
										variant={it.id === selectedProposed?.id ? "default" : "outline"}
										className="h-6 min-w-6 px-1.5 text-[11px]"
										aria-label={`Review item ${index + 1}, intent #${it.id}`}
										aria-pressed={it.id === selectedProposed?.id}
										onClick={() => setSelectedProposedID(it.id)}
									>
										{index + 1}
									</Button>
								))}
							</div>
						</>
					)}
					{reviewAuto && (
						<Badge variant="outline" className="border-emerald-300 bg-emerald-50 text-emerald-700 dark:border-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-400">
							Auto review running · every 15s
						</Badge>
					)}
				</div>
				<div className="flex flex-none items-center gap-2">
					<label htmlFor="review-auto" className="cursor-pointer text-xs font-medium text-muted-foreground">Auto-disposition</label>
					<Switch checked={reviewAuto} disabled={autoToggling} onCheckedChange={toggleAuto} id="review-auto" />
				</div>
			</div>

			          {/* ── Current pending-intent card area: switch with the numbers above instead of stacking cards vertically. ── */}
			{proposed.length > 0 && (
				<div className="border-b">
					<div className="px-4 pt-2.5 text-[11px] font-medium text-amber-700 dark:text-amber-400">
						Pending intent ({selectedProposedIndex + 1} / {proposed.length})
						<span className="ml-2 font-normal text-muted-foreground">Dispatched to the preselected executor immediately on approval</span>
					</div>
					<div className="px-4 py-2.5">
						{proposed.filter((it) => it.id === selectedProposed?.id).map((it) => (
							<div key={it.id} data-testid="review-proposed-card" className="rounded-lg border border-dashed border-amber-500 bg-amber-50 px-3.5 py-2.5 dark:bg-amber-950/20">
								<div className="flex flex-wrap items-center gap-2">
									<span className="font-mono text-sm font-bold text-blue-600 dark:text-blue-400">#{it.id}</span>
									<Badge className="bg-amber-600 text-white">pri={it.priority ?? "?"}</Badge>
									<Badge variant="destructive" className="animate-pulse">Pending</Badge>
									<span className="ml-auto text-xs text-muted-foreground">
										Executor: <b className="text-foreground">{it.pending_executor || "default worker"}</b>
									</span>
									{it.replaces ? (
										<Badge variant="outline" className="border-blue-200 bg-blue-50 text-blue-600 dark:border-blue-900 dark:bg-blue-950/40 dark:text-blue-400" title={`Replaces old intent #${it.replaces} (the old intent is stopped but its row remains in the DB)`}>
											Replaces #{it.replaces}
										</Badge>
									) : null}
								</div>
								<div className="pt-1.5 text-sm leading-6">{it.summary}</div>
								<div className="pb-1 pt-1.5">
									{it.review_note ? (
										<div className="rounded-r-md border-l-[3px] border-amber-500 bg-amber-100/70 px-2.5 py-1.5 text-xs leading-6 text-amber-800 dark:bg-amber-950/40 dark:text-amber-300">
											Pre-screen: {it.review_note}
										</div>
									) : (
										<div className="rounded-r-md border-l-[3px] border-zinc-300 bg-muted px-2.5 py-1.5 text-xs italic text-muted-foreground dark:border-zinc-700">
											Pre-screening… (the review agent adds a suggestion after polling)
										</div>
									)}
								</div>
								<div className="mt-1 flex justify-end gap-2">
									<Button size="sm" variant="outline" className="border-red-200 text-red-600 hover:bg-red-50 dark:border-red-900 dark:hover:bg-red-950/40" onClick={() => { setRejectTarget(it); setRejectReason(prefillRejectReason(it)); }}>
										<XIcon className="mr-1 h-3.5 w-3.5" />Reject
									</Button>
									<Button size="sm" className="bg-green-600 hover:bg-green-700" onClick={() => approve(it)}>
										<CheckIcon className="mr-1 h-3.5 w-3.5" />Approve
									</Button>
								</div>
							</div>
						))}
					</div>
				</div>
			)}

			          {/* ── Message stream (ScrollArea; flex-1 needs min-h-0 for a definite viewport height) ── */}
			<ScrollArea ref={scrollAreaRef} className="min-h-0 flex-1">
				<div className="flex flex-col gap-2.5 px-4 py-3">
					{messages.length === 0 && !awaiting && (
						<div className="py-10 text-center text-xs text-muted-foreground">
							No review messages yet — ask the review agent directly, e.g. "Summarize the current action plan pending review"
						</div>
					)}
					{messages.map((m) => {
						if (m.kind === "user") {
							const text = (m.detail || m.summary).replace(/^\u3010human\u3011/, "");
							return (
								<div key={m.id} className="flex max-w-[82%] flex-row-reverse items-end gap-2 self-end">
									<div className="whitespace-pre-wrap rounded-lg bg-blue-600 px-3 py-2 text-sm leading-6 text-white">
										{text}
										<span className="mt-1 block text-right text-[10px] text-blue-200">{fmtTime(m.created_at)}</span>
									</div>
								</div>
							);
						}
						if (m.is_error) {
							return (
								<div key={m.id} className="mx-auto flex max-w-[88%] items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-xs leading-5 text-red-700 dark:border-red-900 dark:bg-red-950/40 dark:text-red-400">
									<TriangleAlertIcon className="mt-0.5 h-3.5 w-3.5 flex-none" />
									<span>{m.summary}<span className="ml-2 text-[10px] opacity-70">{fmtTime(m.created_at)}</span></span>
								</div>
							);
						}
						if (SYS_RE.test(m.summary)) {
							return (
								<div key={m.id} className="mx-auto max-w-[88%] rounded-full bg-muted px-3.5 py-1 text-center text-[11px] leading-5 text-muted-foreground">
									{m.summary}
								</div>
							);
						}
						return (
							<div key={m.id} className="flex max-w-[82%] items-start gap-2">
								<div className="whitespace-pre-wrap rounded-lg border bg-card px-3 py-2 text-sm leading-6">
									{m.detail || m.summary}
									<span className="mt-1 block text-[10px] text-muted-foreground">review agent · {fmtTime(m.created_at)}</span>
								</div>
							</div>
						);
					})}
					{awaiting && (
						<div>
							<div className="flex items-center gap-2 pl-2 pt-1 text-xs text-muted-foreground">
								<span className="flex gap-1">
									<span className="size-1.5 animate-bounce rounded-full bg-blue-500 [animation-delay:-0.3s]"></span>
									<span className="size-1.5 animate-bounce rounded-full bg-blue-500 [animation-delay:-0.15s]"></span>
									<span className="size-1.5 animate-bounce rounded-full bg-blue-500"></span>
								</span>
								Streaming live…
							</div>
							{awaitSlow && (
								<p className="pl-2 pt-1 text-[11px] text-amber-600 dark:text-amber-400">
									Taking a while — an unreachable LLM endpoint means waiting until timeout; check connectivity under System → Settings → LLM
								</p>
							)}
						</div>
					)}
					<div ref={bottomRef} />
				</div>
			</ScrollArea>

			          {/* Confirmation cards dock above the composer; every write action requires manual confirmation. */}
			{(pending.length > 0 || pendingHasMore) && (
				<div className="border-t bg-amber-50/50 px-4 pt-2.5 dark:bg-amber-950/15">
					{pendingHasMore && (
						<p className="pb-2 text-xs text-amber-700 dark:text-amber-400">
							More than 100 pending requests; only the first page is loaded — please handle the visible ones first.
						</p>
					)}
					{pending.map((p) => (
						<div key={p.id} className="mb-2.5 rounded-xl border border-amber-300/70 bg-card shadow-sm dark:border-amber-900/60">
							<div className="flex items-center gap-1.5 px-3.5 pt-2.5 text-xs font-bold text-amber-700 dark:text-amber-400">
								<TriangleAlertIcon className="h-3.5 w-3.5" />
								Write actions awaiting confirmation (intercept ask)
								<span className="ml-auto font-normal text-muted-foreground">Auto-denied on timeout</span>
							</div>
							<div className="px-3.5 pt-1 text-sm leading-6">The review agent requests to run: <b>{p.tool_name}</b></div>
							<div className="px-3.5 pb-2 pt-1">
								<pre className="max-h-36 overflow-auto rounded-md border bg-muted/50 px-2.5 py-1.5 text-xs text-foreground/80">
									{typeof p.tool_input === "string" ? p.tool_input : JSON.stringify(p.tool_input, null, 2)}
								</pre>
							</div>
							<div className="flex justify-end gap-2 border-t border-amber-100 px-3.5 py-2 dark:border-amber-950">
								<Button size="sm" variant="outline" className="border-red-200 text-red-600 hover:bg-red-50 dark:border-red-900 dark:hover:bg-red-950/40" onClick={() => decidePending(p, "denied")}>
									<XIcon className="mr-1 h-3.5 w-3.5" />Deny
								</Button>
								<Button size="sm" className="bg-green-600 hover:bg-green-700" onClick={() => decidePending(p, "allowed")}>
									<CheckIcon className="mr-1 h-3.5 w-3.5" />Confirm execution
								</Button>
							</div>
						</div>
					))}
				</div>
			)}

			          {/* ── Composer (InputGroup styling consistent with other session panes) ── */}
			<div className="border-t p-3">
				<InputGroup className="min-h-9 has-disabled:opacity-100">
					<InputGroupTextarea
						rows={1}
						aria-label="Chat with the review agent"
						placeholder="Discuss pending intents with the review agent…"
						value={input}
						disabled={sending}
						onChange={(e) => setInput(e.target.value)}
						onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); } }}
						className="max-h-36 min-h-9 overflow-y-auto"
					/>
					<InputGroupAddon align="block-end">
						<InputGroupButton
							className="ml-auto"
							size="icon-xs"
							onClick={send}
							disabled={!input.trim() || sending}
							title="Send message"
							aria-label="Send"
						>
							{sending ? <Loader2Icon className="animate-spin" /> : <ArrowUpIcon />}
						</InputGroupButton>
					</InputGroupAddon>
				</InputGroup>
			</div>

			          {/* ── Reject dialog (reason required) ── */}
			<AlertDialog open={!!rejectTarget} onOpenChange={(o) => !o && setRejectTarget(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Reject intent</AlertDialogTitle>
						<AlertDialogDescription>Rejecting returns the intent for revision (review_status=rejected; node and lineage kept): the planner receives the reason, revises the draft with update_intent, and resubmits automatically.</AlertDialogDescription>
					</AlertDialogHeader>
					{rejectTarget && (
						<div className="rounded-md border bg-muted px-2.5 py-2 text-sm leading-6">
							<b className="font-mono text-blue-600 dark:text-blue-400">#{rejectTarget.id}</b>　{rejectTarget.summary}
						</div>
					)}
					{/* AI pre-screen opinion import: the reject reason defaults to the pre-screen opinion as a draft, editable directly */}
					{rejectTarget?.review_note ? (
						<div className="flex items-center gap-2 text-xs text-amber-700 dark:text-amber-400">
							<span className="rounded border-l-[3px] border-amber-500 bg-amber-50 px-2 py-0.5 dark:bg-amber-950/40">
								AI pre-screen opinion imported — edit freely
							</span>
							<Button
								type="button"
								size="sm"
								variant="ghost"
								className="h-6 px-2 text-xs text-amber-700 hover:bg-amber-50 dark:text-amber-400 dark:hover:bg-amber-950/40"
								onClick={() => setRejectReason(prefillRejectReason(rejectTarget))}
							>
								Re-import
							</Button>
						</div>
					) : null}
					                      {/* field-sizing-fixed: ui/textarea's default field-sizing-content grows with content width;
					                            a long imported pre-screen opinion would push the scrollbar/resize handle past the dialog's right edge */}
					<Textarea
						className="mt-1 h-24 field-sizing-fixed"
						placeholder="Explain the rejection, e.g. wrong direction / target no longer valid / duplicates another intent…"
						value={rejectReason}
						onChange={(e) => setRejectReason(e.target.value)}
					/>
					<p className="text-xs text-muted-foreground">The reason is sent to the planner as a revision_requested event, guiding it to revise this intent with update_intent and resubmit.</p>
					<AlertDialogFooter>
						<Button variant="outline" onClick={() => setRejectTarget(null)}>Cancel</Button>
						<Button variant="destructive" disabled={!rejectReason.trim()} onClick={doReject}>Confirm reject</Button>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}
