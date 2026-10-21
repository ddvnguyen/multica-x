"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { ExternalLink, Timer, Wallet } from "lucide-react";
import type { AgentRuntime } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { runtimeProfileListOptions } from "@multica/core/runtimes";
// Deep import on purpose: the freebuff helpers live behind their own
// entry point so a test that stubs the runtimes barrel does not have to
// know about them.
import {
  freebuffAccountOptions,
  useRequestFreebuffConnect,
} from "@multica/core/runtimes/freebuff";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import { openExternal } from "../../platform";

// Freebuff account card.
//
// A Freebuff runtime runs against an account on the daemon's machine. This
// card shows what that account is worth right now — the open session slot
// and how long it lasts, the price of each model, and the credit left for
// the day and in total — and it is where the account is connected: the
// button asks the daemon to start a browser login, and the one-time URL the
// daemon produces is opened from here.
//
// The card renders nothing for a runtime that is not a Freebuff runtime.

const FREEBUFF_COMMAND_NAME = "codebuddy-freebuff";

// A tick so the countdown in the session row moves on its own.
const TICK_MS = 15_000;

function formatRemaining(remainingMs: number | null | undefined): string | null {
  if (remainingMs == null) return null;
  const totalMinutes = Math.max(0, Math.floor(remainingMs / 60_000));
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  if (hours > 0) return `${hours}h ${minutes}m`;
  return `${minutes}m`;
}

// The price of a model is Freebucks per hour of session. The account host
// has been seen to report it as a number and as an object, so anything that
// is not a plain number is rendered as JSON rather than dropped.
function formatPrice(value: unknown): string | null {
  if (value == null) return null;
  if (typeof value === "number") return String(value);
  if (typeof value === "string") return value;
  if (typeof value === "object") {
    const record = value as Record<string, unknown>;
    for (const key of ["credits", "price", "perHour", "value", "amount"]) {
      const nested = record[key];
      if (typeof nested === "number" || typeof nested === "string") {
        return String(nested);
      }
    }
    return JSON.stringify(value);
  }
  return String(value);
}

export function FreebuffAccountCard({ runtime }: { runtime: AgentRuntime }) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const [, setTick] = useState(0);

  const { data: profiles = [] } = useQuery(runtimeProfileListOptions(wsId));
  const isFreebuff = useMemo(() => {
    if (!runtime.profile_id) return false;
    const profile = profiles.find((p) => p.id === runtime.profile_id);
    return profile?.command_name === FREEBUFF_COMMAND_NAME;
  }, [profiles, runtime.profile_id]);

  const { data: account } = useQuery({
    ...freebuffAccountOptions(runtime.id),
    enabled: isFreebuff,
  });

  const requestConnect = useRequestFreebuffConnect(wsId);

  useEffect(() => {
    if (!isFreebuff) return;
    const id = setInterval(() => setTick((n) => n + 1), TICK_MS);
    return () => clearInterval(id);
  }, [isFreebuff]);

  if (!isFreebuff) return null;

  const status = account?.status ?? null;
  const connect = account?.connect ?? null;
  const credit = status?.credit ?? null;
  const prices = status?.prices ?? null;
  const sessionState = status?.status ?? null;
  const remaining = formatRemaining(status?.remainingMs);
  const connectState = connect?.state ?? "";
  const loginUrl = connect?.login_url ?? null;

  const handleConnect = async () => {
    try {
      await requestConnect.mutateAsync(runtime.id);
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.freebuff.toast_connect_failed),
      );
    }
  };

  const handleOpenLogin = () => {
    if (!loginUrl) return;
    openExternal(loginUrl, { webTarget: "new-tab" });
  };

  return (
    <div className="rounded-lg border">
      <div className="flex items-center justify-between border-b px-4 py-2.5">
        <span className="text-caption font-semibold">
          {t(($) => $.freebuff.title)}
        </span>
        {connectState === "connected" ? (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.freebuff.connected_as, {
              account: connect?.account ?? "",
            })}
          </span>
        ) : loginUrl ? (
          <Button size="sm" variant="outline" onClick={handleOpenLogin}>
            <ExternalLink className="h-3.5 w-3.5" />
            {t(($) => $.freebuff.open_login)}
          </Button>
        ) : (
          <Button
            size="sm"
            variant="outline"
            onClick={handleConnect}
            disabled={requestConnect.isPending || connectState === "requested"}
          >
            {t(($) => $.freebuff.connect)}
          </Button>
        )}
      </div>

      <div className="space-y-3 px-4 py-3">
        {connectState === "failed" && connect?.error ? (
          <p className="text-caption text-destructive">{connect.error}</p>
        ) : null}
        {connectState === "awaiting_login" && !loginUrl ? (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.freebuff.awaiting_login)}
          </p>
        ) : null}

        {/* Session slot — when it opened, when it ends, how long is left. */}
        <div className="flex items-start gap-2.5">
          <Timer className="mt-0.5 h-4 w-4 shrink-0 text-faint-foreground" />
          <div className="min-w-0 flex-1">
            <div className="text-caption font-medium">
              {t(($) => $.freebuff.session_title)}
            </div>
            {sessionState ? (
              <div className="mt-0.5 text-caption text-muted-foreground">
                {t(($) => $.freebuff.session_state, { state: sessionState })}
                {remaining ? ` · ${t(($) => $.freebuff.session_remaining, { time: remaining })}` : ""}
              </div>
            ) : (
              <div className="mt-0.5 text-caption text-muted-foreground">
                {status?.message
                  ? status.message
                  : t(($) => $.freebuff.session_none)}
              </div>
            )}
          </div>
        </div>

        {/* Credit — what is left for the day, and in total. */}
        <div className="flex items-start gap-2.5">
          <Wallet className="mt-0.5 h-4 w-4 shrink-0 text-faint-foreground" />
          <div className="min-w-0 flex-1">
            <div className="text-caption font-medium">
              {t(($) => $.freebuff.credit_title)}
            </div>
            <div className="mt-0.5 text-caption text-muted-foreground">
              {t(($) => $.freebuff.credit_daily, {
                remaining: credit?.dailyRemaining ?? 0,
                limit: credit?.dailyLimit ?? 0,
              })}
            </div>
            <div className="mt-0.5 text-caption text-muted-foreground">
              {t(($) => $.freebuff.credit_total, {
                total: credit?.balance ?? credit?.walletBalance ?? 0,
              })}
            </div>
          </div>
        </div>

        {/* Price per model — what an hour of each model costs. */}
        {prices && Object.keys(prices).length > 0 ? (
          <div className="flex items-start gap-2.5">
            <span className="mt-0.5 h-4 w-4 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="text-caption font-medium">
                {t(($) => $.freebuff.prices_title)}
              </div>
              <div className="mt-1 space-y-0.5">
                {Object.entries(prices).map(([model, price]) => (
                  <div
                    key={model}
                    className="flex items-center justify-between gap-3 text-caption"
                  >
                    <span className="truncate text-muted-foreground">{model}</span>
                    <span className="tabular-nums text-muted-foreground">
                      {formatPrice(price) ?? "—"}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          </div>
        ) : null}
      </div>
    </div>
  );
}
