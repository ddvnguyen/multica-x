import { afterEach, describe, expect, it, vi } from "vitest";
import { unstable_doesMiddlewareMatch } from "next/experimental/testing/server";
import { NextRequest, type NextResponse } from "next/server";
import { MULTICA_LOCALE_HEADER } from "./lib/locale-routing";
import { config, proxy } from "./proxy";

function makeRequest(
  path: string,
  cookies: Record<string, string> = {},
  host = "app.multica.test",
) {
  const cookieHeader = Object.entries(cookies)
    .map(([key, value]) => `${key}=${value}`)
    .join("; ");

  return new NextRequest(`https://${host}${path}`, {
    headers: cookieHeader ? { cookie: cookieHeader } : undefined,
  });
}

async function redirectLocation(
  path: string,
  cookies: Record<string, string> = {},
  host?: string,
) {
  return (await proxy(makeRequest(path, cookies, host))).headers.get("location");
}

function restoreEnv(key: string, value: string | undefined) {
  if (value === undefined) delete process.env[key];
  else process.env[key] = value;
}

async function withoutRuntimeUpstreams(run: () => void | Promise<void>) {
  const previousRemoteApiUrl = process.env.REMOTE_API_URL;
  const previousDocsUrl = process.env.DOCS_URL;
  const previousPublicApiUrl = process.env.NEXT_PUBLIC_API_URL;
  const previousPort = process.env.PORT;
  delete process.env.REMOTE_API_URL;
  delete process.env.DOCS_URL;
  delete process.env.NEXT_PUBLIC_API_URL;
  process.env.PORT = "3000";

  try {
    await run();
  } finally {
    restoreEnv("REMOTE_API_URL", previousRemoteApiUrl);
    restoreEnv("DOCS_URL", previousDocsUrl);
    restoreEnv("NEXT_PUBLIC_API_URL", previousPublicApiUrl);
    restoreEnv("PORT", previousPort);
  }
}

describe("proxy legacy workspace route redirects", () => {
  const sessionCookies = {
    multica_logged_in: "1",
    last_workspace_slug: "acme",
  };

  it.each([
    ["issues", "/acme/issues"],
    ["projects", "/acme/projects"],
    ["agents", "/acme/agents"],
    ["squads", "/acme/squads"],
    ["inbox", "/acme/inbox"],
    ["my-issues", "/acme/my-issues"],
    ["autopilots", "/acme/autopilots"],
    ["runtimes", "/acme/runtimes"],
    ["skills", "/acme/skills"],
    ["settings", "/acme/settings"],
    ["usage", "/acme/usage"],
  ])(
    "redirects legacy /%s URLs through the last workspace slug",
    async (segment, expectedPath) => {
      expect(await redirectLocation(`/${segment}?tab=all`, sessionCookies)).toBe(
        `https://app.multica.test${expectedPath}?tab=all`,
      );
    },
  );

  it("preserves nested legacy paths and query strings", async () => {
    expect(
      await redirectLocation("/squads/squad-123?view=members", sessionCookies),
    ).toBe("https://app.multica.test/acme/squads/squad-123?view=members");
  });

  it("sends logged-out legacy URLs to login", async () => {
    expect(await redirectLocation("/usage?tab=billing")).toBe(
      "https://app.multica.test/login?tab=billing",
    );
  });

  it("sends logged-in legacy URLs without a last workspace cookie to login", async () => {
    // Not root: the root-path rule below leaves "/" on the public site for the
    // official marketing hosts even with a session, so bouncing there would
    // dead-end on the landing page. /login resolves against the workspace list
    // instead. The deep-link query is dropped because feeding a legacy path
    // back through `next` would return here and loop.
    expect(
      await redirectLocation("/squads?view=members", { multica_logged_in: "1" }),
    ).toBe("https://app.multica.test/login");
  });

  it.each(["multica.ai", "www.multica.ai"])(
    "resolves a slugless session off the marketing host %s instead of stranding it",
    async (host) => {
      expect(
        await redirectLocation("/inbox", { multica_logged_in: "1" }, host),
      ).toBe(`https://${host}/login`);
    },
  );

  it("does not redirect workspace-scoped URLs whose first segment is already a slug", async () => {
    expect(await redirectLocation("/acme/squads", sessionCookies)).toBeNull();
  });

  it("redirects app-host root URLs to the last workspace", async () => {
    expect(await redirectLocation("/", sessionCookies)).toBe(
      "https://app.multica.test/acme/issues",
    );
  });

  it.each(["multica.ai", "www.multica.ai"])(
    "does not redirect public marketing root on %s",
    async (host) => {
      expect(await redirectLocation("/", sessionCookies, host)).toBeNull();
    },
  );

  it("still redirects explicit legacy app routes on the public marketing host", async () => {
    expect(await redirectLocation("/issues/ABC-123", sessionCookies, "multica.ai")).toBe(
      "https://multica.ai/acme/issues/ABC-123",
    );
  });
});

describe("proxy runtime upstream rewrites", () => {
  it("matches globally versioned Plugin API requests", async () => {
    expect(config.matcher).toContain("/v1/:path*");
  });

  it("does not rewrite API requests when no runtime API origin is configured", async () => {
    await withoutRuntimeUpstreams(async () => {
      const res = await proxy(makeRequest("/api/config?x=1"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBeNull();
      expect(
        res.headers.get(`x-middleware-request-${MULTICA_LOCALE_HEADER}`),
      ).toBe("en");
    });
  });

  it("does not rewrite Plugin API requests when no runtime API origin is configured", async () => {
    await withoutRuntimeUpstreams(async () => {
      const res = await proxy(makeRequest("/v1/context?x=1"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBeNull();
    });
  });

  it("does not rewrite docs requests when no runtime docs origin is configured", async () => {
    await withoutRuntimeUpstreams(async () => {
      const res = await proxy(makeRequest("/docs/zh"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBeNull();
      expect(
        res.headers.get(`x-middleware-request-${MULTICA_LOCALE_HEADER}`),
      ).toBe("en");
    });
  });

  it("rewrites API requests to the runtime API origin", async () => {
    const previous = process.env.REMOTE_API_URL;
    process.env.REMOTE_API_URL = "http://backend:8080";
    try {
      const res = await proxy(makeRequest("/api/config?x=1"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBe(
        "http://backend:8080/api/config?x=1",
      );
    } finally {
      restoreEnv("REMOTE_API_URL", previous);
    }
  });

  it("rewrites the CLI health probe to the runtime API origin", async () => {
    const previous = process.env.REMOTE_API_URL;
    process.env.REMOTE_API_URL = "http://backend:8080";
    try {
      const res = await proxy(makeRequest("/health"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBe(
        "http://backend:8080/health",
      );
    } finally {
      restoreEnv("REMOTE_API_URL", previous);
    }
  });

  it("rewrites Plugin API requests to the runtime API origin", async () => {
    const previous = process.env.REMOTE_API_URL;
    process.env.REMOTE_API_URL = "http://backend:8080";
    try {
      const res = await proxy(makeRequest("/v1/issues/MUL-6581?x=1"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBe(
        "http://backend:8080/v1/issues/MUL-6581?x=1",
      );
    } finally {
      restoreEnv("REMOTE_API_URL", previous);
    }
  });

  it("rewrites docs requests to the runtime docs origin", async () => {
    const previous = process.env.DOCS_URL;
    process.env.DOCS_URL = "http://docs:4000";
    try {
      const res = await proxy(makeRequest("/docs/zh/agents"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBe(
        "http://docs:4000/docs/zh/agents",
      );
    } finally {
      restoreEnv("DOCS_URL", previous);
    }
  });

  it("rewrites websocket requests to the runtime API origin", async () => {
    const previous = process.env.REMOTE_API_URL;
    process.env.REMOTE_API_URL = "http://backend:8080";
    try {
      const res = await proxy(makeRequest("/ws"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBe(
        "http://backend:8080/ws",
      );
    } finally {
      restoreEnv("REMOTE_API_URL", previous);
    }
  });

  it("does not rewrite frontend auth callback pages", async () => {
    const previous = process.env.REMOTE_API_URL;
    process.env.REMOTE_API_URL = "http://backend:8080";
    try {
      const res = await proxy(makeRequest("/auth/callback"));

      expect(res.status).toBe(200);
      expect(res.headers.get("x-middleware-rewrite")).toBeNull();
      expect(
        res.headers.get(`x-middleware-request-${MULTICA_LOCALE_HEADER}`),
      ).toBe("en");
    } finally {
      restoreEnv("REMOTE_API_URL", previous);
    }
  });
});

describe("proxy root and locale handling", () => {
  it("redirects logged-in root visits to the last workspace", async () => {
    const res = await proxy(
      makeRequest("/", {
        multica_logged_in: "1",
        last_workspace_slug: "acme",
      }),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe(
      "https://app.multica.test/acme/issues",
    );
  });

  it("forwards locale on login requests", async () => {
    const res = await proxy(makeRequest("/login", { "multica-locale": "zh-Hans" }));

    expect(res.status).toBe(200);
    expect(res.headers.get("location")).toBeNull();
    expect(
      res.headers.get(`x-middleware-request-${MULTICA_LOCALE_HEADER}`),
    ).toBe("zh-Hans");
  });
});

describe("proxy marketing-site language URLs", () => {
  const localeOf = (res: Response) => res.headers.get(`x-middleware-request-${MULTICA_LOCALE_HEADER}`);
  const withAcceptLanguage = (path: string, acceptLanguage: string) =>
    new NextRequest(`https://www.multica.ai${path}`, { headers: { "accept-language": acceptLanguage } });

  it.each([
    ["/zh", "/", "zh-Hans"],
    ["/zh/download", "/download", "zh-Hans"],
    ["/ko/usecases/auto-data-analysis", "/usecases/auto-data-analysis", "ko"],
    ["/ja/about?ref=x", "/about?ref=x", "ja"],
  ])("serves %s as %s in its language", async (path, page, locale) => {
    const res = await proxy(makeRequest(path, { "multica-locale": "en" }));

    expect(res.headers.get("location")).toBeNull();
    expect(res.headers.get("x-middleware-rewrite")).toBe(`https://app.multica.test${page}`);
    expect(localeOf(res)).toBe(locale);
  });

  it("keeps English URLs English for visitors who have not picked a language", async () => {
    const res = await proxy(withAcceptLanguage("/download", "zh-CN,zh;q=0.9"));

    expect(res.headers.get("location")).toBeNull();
    expect(res.headers.get("x-middleware-rewrite")).toBeNull();
    expect(localeOf(res)).toBe("en");
  });

  it("keeps locales without a translated site on the English URL", async () => {
    const res = await proxy(makeRequest("/about", { "multica-locale": "fr" }));

    expect(res.headers.get("location")).toBeNull();
    expect(localeOf(res)).toBe("fr");
  });

  it.each([
    ["/", { "multica-locale": "zh-Hans" }, "/zh"],
    ["/download?started=mac", { "multica-locale": "ko" }, "/ko/download?started=mac"],
    ["/about", { "multica-locale": "zh-Hant" }, "/zh/about"],
  ])("sends %s to the language the visitor picked", async (path, cookies, location) => {
    expect(await redirectLocation(path, cookies, "www.multica.ai")).toBe(`https://www.multica.ai${location}`);
  });

  it("leaves app routes under a language-like slug to the app", async () => {
    const res = await proxy(makeRequest("/zh/issues", { "multica-locale": "ja" }));

    expect(res.headers.get("x-middleware-rewrite")).toBeNull();
    expect(localeOf(res)).toBe("ja");
  });

  it("keeps the bare root of an existing workspace with a language slug", async () => {
    const res = await proxy(makeRequest("/ko", { multica_logged_in: "1", last_workspace_slug: "ko" }));

    expect(res.headers.get("x-middleware-rewrite")).toBeNull();
  });
});


describe("upload route bypasses the buffering proxy", () => {
  it.each(["/api/upload-file", "/api/upload-file/"])(
    "excludes %s from Proxy matching",
    async (url) => expect(unstable_doesMiddlewareMatch({ config, url })).toBe(false),
  );

  it.each(["/api", "/api/config", "/api/upload-file-extra", "/v1/issues", "/auth/token", "/docs/intro", "/login"])(
    "keeps the existing proxy behavior for %s",
    async (url) => expect(unstable_doesMiddlewareMatch({ config, url })).toBe(true),
  );
});

describe("proxy MMX auth bypass (M-404)", () => {
  const OLD = { ...process.env };

  function setBypassEnv(env: Record<string, string | undefined>) {
    for (const k of ["MMX_PWA", "MMX_AUTH_BYPASS", "MMX_BIND_HOST"]) delete process.env[k];
    Object.assign(process.env, env);
  }

  afterEach(() => {
    process.env = { ...OLD };
    vi.unstubAllGlobals();
  });

  it("stays off unless MMX PWA build and flag are both on", async () => {
    const { isAuthBypassEnabled } = await import("./proxy");
    setBypassEnv({ MMX_AUTH_BYPASS: "0" });
    expect(isAuthBypassEnabled()).toBe(false);
    setBypassEnv({ MMX_AUTH_BYPASS: undefined });
    expect(isAuthBypassEnabled()).toBe(false);
    // Deliberately not gated on the MMX_PWA build flag (build-time
    // branding, not runtime env): the lane flag alone enables it.
    setBypassEnv({ MMX_PWA: undefined, MMX_AUTH_BYPASS: "1" });
    expect(isAuthBypassEnabled()).toBe(true);
  });

  it("signs in once and sets the session cookie", async () => {
    const { proxy } = await import("./proxy");
    setBypassEnv({
      MMX_PWA: "1",
      MMX_AUTH_BYPASS: "1",
      MMX_BIND_HOST: "127.0.0.1",
      REMOTE_API_URL: "http://127.0.0.1:4390",
      MULTICA_DEV_VERIFICATION_CODE: "002608",
    });
    // Silence the one success line the bypass logs per sign-in.
    vi.spyOn(console, "log").mockImplementation(() => {});
    const calls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        calls.push(url);
        if (url.endsWith("/auth/send-code")) return new Response("{}", { status: 200 });
        const headers = new Headers();
        headers.append("set-cookie", "multica_auth=tok; Path=/; HttpOnly");
        return new Response(JSON.stringify({ token: "tok", user: { id: "1" } }), {
          status: 200,
          headers,
        });
      }),
    );
    const res = (await proxy(makeRequest("/inbox"))) as NextResponse;
    expect(calls).toEqual([
      "http://127.0.0.1:4390/auth/send-code",
      "http://127.0.0.1:4390/auth/verify-code",
    ]);
    // Same-request continuation: the bypass replays the backend session
    // onto the request and routes it as authenticated (/inbox has no slug
    // yet, so it lands on /login to resolve the workspace list) — while
    // the response carries the session cookies to the browser.
    const location = res.headers.get("location") ?? "";
    expect(location).toContain("/login");
    const rawSetCookie = res.headers.get("set-cookie") ?? "";
    expect(rawSetCookie).toContain("multica_logged_in=1");
    expect(rawSetCookie).toContain("multica_auth=tok");
  });

  it("does not loop on logout: with the session cookie it falls through", async () => {
    const { proxy } = await import("./proxy");
    setBypassEnv({
      MMX_PWA: "1",
      MMX_AUTH_BYPASS: "1",
      MMX_BIND_HOST: "127.0.0.1",
      REMOTE_API_URL: "http://127.0.0.1:4390",
      MULTICA_DEV_VERIFICATION_CODE: "002608",
    });
    const fetchSpy = vi.fn(async () => new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchSpy);
    // /auth/* is excluded from the bypass; the logout POST passes through.
    await proxy(makeRequest("/auth/logout", { multica_logged_in: "1" }));
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("falls through to login when the backend refuses", async () => {
    const { proxy } = await import("./proxy");
    setBypassEnv({
      MMX_PWA: "1",
      MMX_AUTH_BYPASS: "1",
      MMX_BIND_HOST: "127.0.0.1",
      REMOTE_API_URL: "http://127.0.0.1:4390",
      MULTICA_DEV_VERIFICATION_CODE: "002608",
    });
    vi.stubGlobal("fetch", vi.fn(async () => new Response("nope", { status: 500 })));
    const res = (await proxy(makeRequest("/login"))) as NextResponse;
    expect(res.status).not.toBe(500);
    expect(res.cookies.get("multica_logged_in")).toBeUndefined();
  });

  it("refuses a non-private bind", async () => {
    const { proxy } = await import("./proxy");
    setBypassEnv({
      MMX_PWA: "1",
      MMX_AUTH_BYPASS: "1",
      MMX_BIND_HOST: "0.0.0.0",
      REMOTE_API_URL: "http://127.0.0.1:4390",
      MULTICA_DEV_VERIFICATION_CODE: "002608",
    });
    const fetchSpy = vi.fn(async () => new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchSpy);
    await proxy(makeRequest("/inbox"));
    expect(fetchSpy).not.toHaveBeenCalled();
  });
});
