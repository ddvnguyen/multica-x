"use client";

import Link from "next/link";

/**
 * Offline fallback (PWA shell only). The service worker serves this cached
 * page when a navigation has no network. No data is cached by design — issue
 * and agent state is live and mutating — so this page says offline and links
 * back to the inbox for retry.
 */
export default function Offline() {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center gap-6 px-6 py-24 text-center">
      <p className="text-sm font-medium text-muted-foreground">Offline</p>
      <h1 className="text-2xl font-semibold tracking-tight">You are offline</h1>
      <p className="max-w-md text-sm text-muted-foreground">
        MMX needs a connection to load your work. Check your network and try again.
      </p>
      <Link
        href="/inbox"
        className="mt-2 inline-flex h-9 items-center rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground"
      >
        Retry
      </Link>
    </main>
  );
}
