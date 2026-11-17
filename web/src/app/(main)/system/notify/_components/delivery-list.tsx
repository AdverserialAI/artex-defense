/**
 * delivery-list.tsx: notification delivery records table — filter by channel and status with paging; failed items can be resent manually.
 * Main functions/types:
 *   - DeliveryList: delivery records table component (loading, filtering, paging, resend)
 *   - formatTime: formats ISO time for local display
 */
"use client";

import * as React from "react";

import { RefreshCwIcon, RotateCcwIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import { statusMeta, toneClasses } from "@/lib/status";
import type { NotificationChannel, NotificationDelivery } from "@/lib/types";

// DeliveryList is the delivery records table: filterable by channel and status; failed items can be resent manually.
export function DeliveryList({ channels }: { channels: NotificationChannel[] }) {
  const [rows, setRows] = React.useState<NotificationDelivery[]>([]);
  const [total, setTotal] = React.useState(0);
  const [page, setPage] = React.useState(1);
  const [channelID, setChannelID] = React.useState<number | undefined>(undefined);
  const [state, setState] = React.useState<string | undefined>(undefined);
  const [loading, setLoading] = React.useState(false);
  const loadSeq = React.useRef(0);
  const pageSize = 50;

  const load = React.useCallback(() => {
    const seq = ++loadSeq.current;
    setLoading(true);
    api
      .notifyDeliveries({ channelId: channelID, state, page, pageSize })
      .then((r) => {
        if (loadSeq.current !== seq) return;
        setRows(r.deliveries);
        setTotal(r.total);
      })
      .catch((e) => {
        if (loadSeq.current === seq) toast.error("Failed to load delivery records: " + (e as Error).message);
      })
      .finally(() => {
        if (loadSeq.current === seq) setLoading(false);
      });
  }, [channelID, state, page]);
  React.useEffect(() => {
    load();
  }, [load]);

  async function retry(id: number) {
    try {
      await api.notifyRetryDelivery(id);
      toast.success("Re-queued");
      load();
    } catch (e) {
      toast.error("Resend failed: " + (e as Error).message);
    }
  }

  const maxPage = Math.max(1, Math.ceil(total / pageSize));
  React.useEffect(() => {
    // When deletes, resends, or filters shrink the total, automatically return to the last valid page.
    if (page > maxPage) setPage(maxPage);
  }, [maxPage, page]);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={channelID ? String(channelID) : "all"}
          onValueChange={(v) => {
            setPage(1);
            setChannelID(v === "all" ? undefined : Number(v));
          }}
        >
          <SelectTrigger size="sm" className="w-44">
            <SelectValue placeholder="All channels" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All channels</SelectItem>
            {channels.map((c) => (
              <SelectItem key={c.id} value={String(c.id)}>
                {c.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={state ?? "all"}
          onValueChange={(v) => {
            setPage(1);
            setState(v === "all" ? undefined : v);
          }}
        >
          <SelectTrigger size="sm" className="w-32">
            <SelectValue placeholder="All statuses" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All statuses</SelectItem>
            {["pending", "sending", "sent", "failed", "skipped"].map((s) => (
              <SelectItem key={s} value={s}>
                {statusMeta("delivery", s).label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button size="sm" variant="outline" onClick={load} disabled={loading}>
          <RefreshCwIcon className={loading ? "animate-spin" : ""} /> Refresh
        </Button>
        <span className="text-muted-foreground ml-auto text-xs">{total} total</span>
      </div>

      <Card className="py-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-40">Time</TableHead>
              <TableHead>Finding</TableHead>
              <TableHead className="w-40">Channel</TableHead>
              <TableHead className="w-24">Status</TableHead>
              <TableHead className="w-16">Attempts</TableHead>
              <TableHead>Error</TableHead>
              <TableHead className="w-20" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={7} className="text-muted-foreground py-8 text-center">
                  {loading ? "Loading…" : "No delivery records"}
                </TableCell>
              </TableRow>
            ) : (
              rows.map((d) => (
                <TableRow key={d.id}>
                  <TableCell className="text-muted-foreground text-xs whitespace-nowrap">
                    {formatTime(d.created_at)}
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-2">
                      <Badge variant="outline" className={toneClasses[statusMeta("severity", d.severity).tone]}>
                        {statusMeta("severity", d.severity).label}
                      </Badge>
                      <span className="truncate text-sm">{d.title || "(untitled)"}</span>
                      {d.event_kind === "finding_status_changed" && (
                        <Badge variant="outline" className="shrink-0">
                          Status change
                        </Badge>
                      )}
                    </div>
                  </TableCell>
                  <TableCell className="text-sm">{d.channel_name}</TableCell>
                  <TableCell>
                    <Badge variant="outline" className={toneClasses[statusMeta("delivery", d.state).tone]}>
                      {statusMeta("delivery", d.state).label}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground text-sm">{d.attempts}</TableCell>
                  <TableCell className="text-muted-foreground max-w-md text-xs break-all">{d.last_error}</TableCell>
                  <TableCell>
                    {/* Only failed/skipped items get a resend entry: resending delivered ones would duplicate notifications. */}
                    {(d.state === "failed" || d.state === "skipped") && (
                      <Button size="sm" variant="outline" onClick={() => retry(d.id)}>
                        <RotateCcwIcon /> Resend
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </Card>

      {maxPage > 1 && (
        <div className="flex items-center justify-end gap-2">
          <Button size="sm" variant="outline" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
            Previous
          </Button>
          <span className="text-muted-foreground text-sm">
            {page} / {maxPage}
          </span>
          <Button size="sm" variant="outline" disabled={page >= maxPage} onClick={() => setPage((p) => p + 1)}>
            Next
          </Button>
        </div>
      )}
    </div>
  );
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString("en-US", { hour12: false });
}
