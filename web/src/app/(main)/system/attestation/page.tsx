"use client";

import * as React from "react";

import {
  CheckCircle2Icon,
  DownloadIcon,
  ExternalLinkIcon,
  Loader2Icon,
  ShieldCheckIcon,
  XCircleIcon,
} from "lucide-react";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { sha256Hex } from "@/lib/sha256";

const PHALA_VERIFY_URL = "https://cloud-api.phala.com/api/v1/attestations/verify";
const DEFAULT_DSTACK_VERIFIER_URL = process.env.NEXT_PUBLIC_DSTACK_VERIFIER_URL || "";

type QuoteEvidence = {
  quote: string;
  attestation?: string;
  event_log: string;
  report_data?: string;
  vm_config?: string;
};

type EventLogEntry = {
  imr: number;
  event: string;
  event_payload: string;
  digest: string;
};

type TcbInfo = {
  mrtd?: string;
  rtmr0?: string;
  rtmr1?: string;
  rtmr2?: string;
  rtmr3?: string;
  app_compose: string;
};

type InfoEvidence = {
  app_id?: string;
  instance_id?: string;
  app_name?: string;
  device_id?: string;
  compose_hash?: string;
  os_image_hash?: string;
  key_provider_info?: string;
  cloud_vendor?: string;
  cloud_product?: string;
  tcb_info: string | TcbInfo;
};

type Bundle = {
  nonce: string;
  quote: QuoteEvidence;
  info: InfoEvidence;
  tcb: TcbInfo;
  appCompose: string;
  dockerCompose: string;
  images: string[];
  events: EventLogEntry[];
};

type CheckState = boolean | null;

function normalizeHex(value: string | undefined) {
  return (value ?? "").trim().toLowerCase().replace(/^0x/, "");
}

async function responseJSON<T>(response: Response): Promise<T> {
  const payload = await response.json();
  if (!response.ok) throw new Error(payload.error || `HTTP ${response.status}`);
  return payload as T;
}

function parseBundle(nonce: string, quote: QuoteEvidence, info: InfoEvidence): Bundle {
  const tcb = typeof info.tcb_info === "string" ? (JSON.parse(info.tcb_info) as TcbInfo) : info.tcb_info;
  const events = typeof quote.event_log === "string" ? (JSON.parse(quote.event_log) as EventLogEntry[]) : [];
  const appCompose = JSON.parse(tcb.app_compose) as { docker_compose_file?: string };
  const dockerCompose = appCompose.docker_compose_file || "No Docker Compose file was supplied by the guest agent.";
  const images = Array.from(dockerCompose.matchAll(/^\s*image:\s*["']?([^\s"']+)["']?\s*$/gm), (match) => match[1]);
  return { nonce, quote, info, tcb, appCompose: tcb.app_compose, dockerCompose, images, events };
}

function CheckBadge({ value, pending }: { value: CheckState; pending: boolean }) {
  if (pending) return <Badge variant="outline">Checking</Badge>;
  if (value === null) return <Badge variant="outline">Not checked</Badge>;
  return value ? <Badge>Passed</Badge> : <Badge variant="destructive">Failed</Badge>;
}

export default function AttestationPage() {
  const [bundle, setBundle] = React.useState<Bundle | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [hardwareValid, setHardwareValid] = React.useState<CheckState>(null);
  const [nonceValid, setNonceValid] = React.useState<CheckState>(null);
  const [composeValid, setComposeValid] = React.useState<CheckState>(null);
  const [calculatedHash, setCalculatedHash] = React.useState("");
  const [error, setError] = React.useState("");
  const [warning, setWarning] = React.useState("");
  const [verifierURL, setVerifierURL] = React.useState(DEFAULT_DSTACK_VERIFIER_URL);

  async function verifyRuntime() {
    setLoading(true);
    setError("");
    setWarning("");
    setHardwareValid(null);
    setNonceValid(null);
    setComposeValid(null);
    setCalculatedHash("");
    try {
      const nonceBytes = crypto.getRandomValues(new Uint8Array(32));
      const nonce = Array.from(nonceBytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
      const [quoteResponse, infoResponse] = await Promise.all([
        fetch(`/attestation?reportData=${nonce}`, { cache: "no-store" }),
        fetch("/info", { cache: "no-store" }),
      ]);
      const quote = await responseJSON<QuoteEvidence>(quoteResponse);
      const info = await responseJSON<InfoEvidence>(infoResponse);
      const nextBundle = parseBundle(nonce, quote, info);
      setBundle(nextBundle);

      const nonceMatches = normalizeHex(quote.report_data).startsWith(nonce);
      setNonceValid(nonceMatches);

      const composeEvent = nextBundle.events.find((event) => event.event === "compose-hash");
      const digest = await sha256Hex(nextBundle.appCompose);
      setCalculatedHash(digest);
      setComposeValid(!!composeEvent && normalizeHex(composeEvent.event_payload) === digest);

      try {
        if (quote.attestation) {
          if (!verifierURL.trim()) throw new Error("Enter the URL of an external dstack-verifier.");
          const verifyResponse = await fetch(verifierURL.trim(), {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ attestation: quote.attestation }),
          });
          const result = await responseJSON<{
            is_valid?: boolean;
            reason?: string | null;
            details?: { report_data?: string };
          }>(verifyResponse);
          setHardwareValid(result.is_valid === true);
          setNonceValid(normalizeHex(result.details?.report_data).startsWith(nonce));
          if (result.is_valid !== true)
            setWarning(result.reason || "The external dstack-verifier rejected the evidence.");
        } else {
          const verifyResponse = await fetch(PHALA_VERIFY_URL, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ hex: quote.quote }),
          });
          const result = await responseJSON<{ quote?: { verified?: boolean } }>(verifyResponse);
          setHardwareValid(result.quote?.verified === true);
          if (result.quote?.verified !== true) setWarning("Phala's verifier rejected the TDX quote.");
        }
      } catch (verifyError) {
        setHardwareValid(null);
        setWarning(
          `The browser could not complete external verification: ${(verifyError as Error).message}. Download the evidence and verify it with dstack-verifier or dcap-qvl instead.`,
        );
      }
    } catch (fetchError) {
      setBundle(null);
      setError((fetchError as Error).message);
    } finally {
      setLoading(false);
    }
  }

  function downloadEvidence() {
    if (!bundle) return;
    const evidence = {
      generated_at: new Date().toISOString(),
      nonce: bundle.nonce,
      attestation: bundle.quote,
      info: bundle.info,
      browser_checks: {
        hardware_quote_valid: hardwareValid,
        nonce_matches: nonceValid,
        compose_hash_matches: composeValid,
        calculated_compose_hash: calculatedHash,
      },
    };
    const url = URL.createObjectURL(new Blob([JSON.stringify(evidence, null, 2)], { type: "application/json" }));
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `artex-defense-attestation-${Date.now()}.json`;
    anchor.click();
    URL.revokeObjectURL(url);
  }

  const fullyVerified = hardwareValid === true && nonceValid === true && composeValid === true;
  const measurements = bundle
    ? [
        ["MRTD", bundle.tcb.mrtd],
        ["RTMR0", bundle.tcb.rtmr0],
        ["RTMR1", bundle.tcb.rtmr1],
        ["RTMR2", bundle.tcb.rtmr2],
        ["RTMR3", bundle.tcb.rtmr3],
        ["OS image", bundle.info.os_image_hash],
      ]
    : [];

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="flex items-center gap-2 font-semibold text-2xl">
            <ShieldCheckIcon className="size-6" /> Runtime attestation
          </h1>
          <p className="mt-1 max-w-3xl text-muted-foreground text-sm">
            Generate fresh Intel TDX evidence for ARTEX Defense, verify the hardware quote, and confirm that the
            attested Compose hash describes this deployment. Verification runs in your browser, outside the
            confidential VM.
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" disabled={!bundle || loading} onClick={downloadEvidence}>
            <DownloadIcon /> Download evidence
          </Button>
          <Button disabled={loading} onClick={verifyRuntime}>
            {loading ? <Loader2Icon className="animate-spin" /> : <ShieldCheckIcon />}
            {loading ? "Verifying…" : "Verify runtime"}
          </Button>
        </div>
      </div>

      <Alert>
        <ShieldCheckIcon />
        <AlertTitle>What this proves—and what it does not</AlertTitle>
        <AlertDescription>
          A passing result binds your fresh nonce and the application Compose manifest to genuine TEE evidence through
          an external verifier. It does not prove that the source code is safe, that every dependency is trusted, or
          that the KMS and governance are honest. For high-assurance use, independently replay RTMR3, compare image
          digests with an audited release, and verify the platform with dstack-verifier.
        </AlertDescription>
      </Alert>

      {bundle?.quote.attestation && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">External dstack-verifier</CardTitle>
            <CardDescription>
              GCP evidence includes both Intel TDX and Google vTPM quotes. Verification must run outside this CVM.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Label htmlFor="verifier-url">Verification endpoint</Label>
            <Input
              id="verifier-url"
              value={verifierURL}
              onChange={(event) => setVerifierURL(event.target.value)}
              placeholder="https://verifier.example.com/verify"
              spellCheck={false}
            />
          </CardContent>
        </Card>
      )}

      {error && (
        <Alert variant="destructive">
          <XCircleIcon />
          <AlertTitle>Attestation evidence is unavailable</AlertTitle>
          <AlertDescription>
            {error}. This endpoint works when ARTEX runs in dstack with <code>/var/run/dstack.sock</code> mounted. It is
            expected to fail in an ordinary local Docker environment.
          </AlertDescription>
        </Alert>
      )}
      {warning && (
        <Alert>
          <XCircleIcon />
          <AlertTitle>Hardware verification is incomplete</AlertTitle>
          <AlertDescription>{warning}</AlertDescription>
        </Alert>
      )}
      {fullyVerified && (
        <Alert>
          <CheckCircle2Icon />
          <AlertTitle>Runtime attestation passed</AlertTitle>
          <AlertDescription>
            The TDX quote, fresh nonce, and application Compose hash all passed their respective checks.
          </AlertDescription>
        </Alert>
      )}

      <div className="grid gap-4 md:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">TDX hardware quote</CardTitle>
            <CardDescription>
              Checked directly by your browser through the configured external verifier.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <CheckBadge value={hardwareValid} pending={loading} />
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Freshness challenge</CardTitle>
            <CardDescription>Prevents replay by binding the quote to a new random 32-byte nonce.</CardDescription>
          </CardHeader>
          <CardContent>
            <CheckBadge value={nonceValid} pending={loading} />
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Application Compose hash</CardTitle>
            <CardDescription>Compares SHA-256 of app-compose.json with the measured boot event.</CardDescription>
          </CardHeader>
          <CardContent>
            <CheckBadge value={composeValid} pending={loading} />
          </CardContent>
        </Card>
      </div>

      {bundle && (
        <>
          <Card>
            <CardHeader>
              <CardTitle>Measured workload</CardTitle>
              <CardDescription>
                {bundle.info.app_name || "ARTEX"} · {bundle.info.cloud_vendor || "Unknown cloud"}{" "}
                {bundle.info.cloud_product || ""}
              </CardDescription>
            </CardHeader>
            <CardContent className="grid gap-4 text-sm md:grid-cols-2">
              <div>
                <div className="text-muted-foreground">Application ID</div>
                <code className="break-all">{bundle.info.app_id || "Not supplied"}</code>
              </div>
              <div>
                <div className="text-muted-foreground">Instance ID</div>
                <code className="break-all">{bundle.info.instance_id || "Not supplied"}</code>
              </div>
              <div>
                <div className="text-muted-foreground">Attested Compose hash</div>
                <code className="break-all">{bundle.info.compose_hash || calculatedHash}</code>
              </div>
              <div>
                <div className="text-muted-foreground">Key provider</div>
                <code className="break-all">{bundle.info.key_provider_info || "Not supplied"}</code>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Container images</CardTitle>
              <CardDescription>
                Image references declared in the measured Compose file. Digests are immutable; tags are mutable and need
                separate review.
              </CardDescription>
            </CardHeader>
            <CardContent>
              {bundle.images.length ? (
                <ul className="flex flex-col gap-2">
                  {bundle.images.map((image) => (
                    <li key={image} className="flex flex-wrap items-center gap-2">
                      <code className="break-all text-sm">{image}</code>
                      <Badge variant={image.includes("@sha256:") ? "default" : "outline"}>
                        {image.includes("@sha256:") ? "Digest pinned" : "Mutable tag"}
                      </Badge>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-muted-foreground text-sm">No image references were found.</p>
              )}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Platform measurements</CardTitle>
              <CardDescription>Measurements supplied by the guest agent and covered by the evidence.</CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              {measurements.map(([label, value]) => (
                <div key={label}>
                  <div className="text-muted-foreground text-xs">{label}</div>
                  <code className="break-all text-xs">{value || "Not supplied"}</code>
                </div>
              ))}
            </CardContent>
          </Card>

          <details className="rounded-lg border p-4">
            <summary className="cursor-pointer font-medium text-sm">Docker Compose measured at boot</summary>
            <pre className="mt-4 max-h-[32rem] overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted p-4 text-xs">
              {bundle.dockerCompose}
            </pre>
          </details>
          <details className="rounded-lg border p-4">
            <summary className="cursor-pointer font-medium text-sm">Raw app-compose.json</summary>
            <pre className="mt-4 max-h-[32rem] overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted p-4 text-xs">
              {JSON.stringify(JSON.parse(bundle.appCompose), null, 2)}
            </pre>
          </details>
        </>
      )}

      <div className="text-muted-foreground text-sm">
        Independent verification resources:{" "}
        <a
          className="inline-flex items-center gap-1 text-primary underline"
          href="https://docs.phala.com/phala-cloud/attestation/verification-guide"
          target="_blank"
          rel="noreferrer"
        >
          Phala verification guide <ExternalLinkIcon className="size-3" />
        </a>{" "}
        ·{" "}
        <a
          className="inline-flex items-center gap-1 text-primary underline"
          href="https://proof.t16z.com/"
          target="_blank"
          rel="noreferrer"
        >
          TEE Attestation Explorer <ExternalLinkIcon className="size-3" />
        </a>
      </div>
    </div>
  );
}
