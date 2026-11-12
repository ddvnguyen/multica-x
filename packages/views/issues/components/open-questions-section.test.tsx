import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "@multica/core/api";
import type { Question } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { OpenQuestionsSection } from "./open-questions-section";

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = (await importOriginal()) as Record<string, unknown>;
  return {
    ...actual,
    api: {
      listIssueQuestions: vi.fn(),
      answerQuestion: vi.fn(),
    },
  };
});

const QUESTION: Question = {
  id: "question-1",
  group_id: "group-1",
  issue_id: "issue-1",
  header: "Runtime choice",
  prompt: "Which runtime should this use?",
  options: [
    { label: "CodeBuddy", description: "Existing wrapper" },
    { label: "Freebuff", description: "Free account" },
  ],
  multi_select: false,
  status: "open",
  expires_at: "2026-11-12T13:00:00Z",
  created_at: "2026-11-12T12:00:00Z",
};

function renderSection() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  renderWithI18n(
    <QueryClientProvider client={client}>
      <OpenQuestionsSection issueId="issue-1" />
    </QueryClientProvider>,
  );
  return client;
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("OpenQuestionsSection", () => {
  it("answers the same question when an option is chosen", async () => {
    vi.mocked(api.listIssueQuestions).mockResolvedValue([QUESTION]);
    vi.mocked(api.answerQuestion).mockResolvedValue({ ...QUESTION, status: "answered" });
    renderSection();
    await waitFor(() => expect(api.listIssueQuestions).toHaveBeenCalledWith("issue-1", "open"));

    expect(await screen.findByText("Which runtime should this use?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Freebuff/ }));
    await waitFor(() => expect(api.answerQuestion).toHaveBeenCalledWith("question-1", "Freebuff"));
  });
});
