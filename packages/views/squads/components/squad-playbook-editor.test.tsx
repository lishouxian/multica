// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enSquads from "../../locales/en/squads.json";
import { SquadPlaybookEditor } from "./squad-playbook-editor";

const resources = {
  en: { squads: enSquads },
};

function renderEditor(overrides: Partial<Parameters<typeof SquadPlaybookEditor>[0]> = {}) {
  const props: Parameters<typeof SquadPlaybookEditor>[0] = {
    initialDefinition: { version: 1, steps: [] },
    canManage: true,
    active: true,
    saving: false,
    disabling: false,
    starting: false,
    onDirtyChange: vi.fn(),
    onSave: vi.fn().mockResolvedValue(undefined),
    onDisable: vi.fn().mockResolvedValue(undefined),
    onStart: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
  render(
    <I18nProvider locale="en" resources={resources}>
      <SquadPlaybookEditor {...props} />
    </I18nProvider>,
  );
  return props;
}

describe("SquadPlaybookEditor", () => {
  it("rejects malformed JSON before calling save", async () => {
    const props = renderEditor();
    fireEvent.change(screen.getByRole("textbox", { name: "Playbook definition" }), {
      target: { value: "{" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save version" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Definition must be valid JSON.",
    );
    expect(props.onSave).not.toHaveBeenCalled();
  });

  it("saves a parsed definition and starts a run", async () => {
    const props = renderEditor();
    const definition = {
      version: 1,
      steps: [{ key: "triage" }],
    };
    fireEvent.change(screen.getByRole("textbox", { name: "Playbook definition" }), {
      target: { value: JSON.stringify(definition) },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save version" }));

    await waitFor(() => expect(props.onSave).toHaveBeenCalledWith(definition));

    fireEvent.change(screen.getByLabelText("Root issue ID"), {
      target: { value: "11111111-1111-1111-1111-111111111111" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));

    await waitFor(() =>
      expect(props.onStart).toHaveBeenCalledWith(
        "11111111-1111-1111-1111-111111111111",
      ),
    );
  });

  it("can re-enable an unchanged saved definition", async () => {
    const props = renderEditor({ active: false });
    fireEvent.click(screen.getByRole("button", { name: "Enable playbook" }));

    await waitFor(() =>
      expect(props.onSave).toHaveBeenCalledWith({ version: 1, steps: [] }),
    );
  });
});
