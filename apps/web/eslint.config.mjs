import nextConfig from "@multica/eslint-config/next";

export default [
  ...nextConfig,
  { ignores: [".next/", ".source/"] },
  {
    files: ["**/*.test.{ts,tsx}", "**/test/**/*.{ts,tsx}"],
    rules: {
      "react/display-name": "off",
    },
  },
  // MMX PWA service worker (M-390): plain browser script, no imports. The
  // serviceworker globals (self/caches/fetch/URL/...) are provided by the
  // worker runtime, so no-undef must not fire for them here.
  {
    files: ["public/sw.js"],
    languageOptions: {
      globals: {
        self: "readonly",
        caches: "readonly",
        fetch: "readonly",
        URL: "readonly",
        Request: "readonly",
        Response: "readonly",
        Headers: "readonly",
        console: "readonly",
        setTimeout: "readonly",
        clearTimeout: "readonly",
        addEventListener: "readonly",
        skipWaiting: "readonly",
        clients: "readonly",
      },
    },
  },
];
