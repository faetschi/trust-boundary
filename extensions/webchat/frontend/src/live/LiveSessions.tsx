// The rail's sessions section in live mode: the sessions the product service lists for this user, with project, provider
// and mode, and a creator. Every value is the service's, drawn as text. Choosing a session opens it (a snapshot, then the
// event stream from its cursor); creating one sends a create request under a fresh key and opens the result.

import { useId, useState, type FormEvent } from "react";
import { useAnnouncer } from "../ui/LiveRegion";
import { Chip } from "../ui/Chip";
import { Disclosure } from "../ui/Disclosure";
import { Notice } from "../ui/Notice";
import { ActionNotice } from "./ActionNotice";
import type { CreateParams } from "./client";
import { useLive } from "./context";
import styles from "./Live.module.css";

export interface CreateFields {
  readonly project: string;
  readonly provider: string;
  readonly mode: string;
  readonly hostCalls: string;
  readonly tokens: string;
  readonly seconds: string;
  readonly readOnly: boolean;
}

export const DEFAULT_FIELDS: CreateFields = {
  project: "",
  provider: "",
  mode: "review",
  hostCalls: "50",
  tokens: "100000",
  seconds: "600",
  readOnly: false,
};

// The service accepts printable ASCII without spaces for a project and a provider profile, and positive whole numbers for
// the budget. The service still decides whether it knows them and whether the budget fits its limits.
const IDENTITY = /^[\x21-\x7e]{1,128}$/;
const WHOLE = /^[1-9][0-9]{0,11}$/;

// The create request, or the first problem in words.
export function createParams(fields: CreateFields): CreateParams | string {
  if (!IDENTITY.test(fields.project)) return "Project is required: 1 to 128 printable characters, no spaces.";
  if (!IDENTITY.test(fields.provider)) return "Provider profile is required: 1 to 128 printable characters, no spaces.";
  for (const [name, value] of [
    ["Host calls", fields.hostCalls],
    ["Tokens", fields.tokens],
    ["Seconds", fields.seconds],
  ] as const) {
    if (!WHOLE.test(value)) return `${name} must be a positive whole number.`;
  }
  return {
    projectId: fields.project,
    providerProfile: fields.provider,
    publicationMode: fields.mode,
    hostCalls: Number(fields.hostCalls),
    tokens: Number(fields.tokens),
    durationSeconds: Number(fields.seconds),
    readOnly: fields.readOnly,
  };
}

function CreateForm({ projects, providers }: { readonly projects: readonly string[]; readonly providers: readonly string[] }) {
  const live = useLive();
  const [fields, setFields] = useState<CreateFields>(DEFAULT_FIELDS);
  const [problem, setProblem] = useState<string | null>(null);
  const id = useId();
  const announcer = useAnnouncer();
  if (live === null) return null;
  const sending = live.model.action.status === "sending";
  const change = (patch: Partial<CreateFields>) => setFields((current) => ({ ...current, ...patch }));

  function submit(event: FormEvent): void {
    event.preventDefault();
    const params = createParams(fields);
    if (typeof params === "string") {
      setProblem(params);
      announcer.announce(params);
      return;
    }
    setProblem(null);
    void live?.controller.createSession(params);
  }

  return (
    <form className={styles.form} onSubmit={submit} aria-label="New session">
      <div className={styles.field}>
        <label className={styles.label} htmlFor={`${id}-project`}>
          Project
        </label>
        <input
          id={`${id}-project`}
          className={styles.input}
          list={`${id}-projects`}
          value={fields.project}
          onChange={(event) => change({ project: event.target.value })}
          autoComplete="off"
          spellCheck={false}
        />
        <datalist id={`${id}-projects`}>
          {projects.map((project) => (
            <option key={project} value={project} />
          ))}
        </datalist>
      </div>
      <div className={styles.field}>
        <label className={styles.label} htmlFor={`${id}-provider`}>
          Provider profile
        </label>
        <input
          id={`${id}-provider`}
          className={styles.input}
          list={`${id}-providers`}
          value={fields.provider}
          onChange={(event) => change({ provider: event.target.value })}
          autoComplete="off"
          spellCheck={false}
        />
        <datalist id={`${id}-providers`}>
          {providers.map((provider) => (
            <option key={provider} value={provider} />
          ))}
        </datalist>
      </div>
      <div className={styles.field}>
        <label className={styles.label} htmlFor={`${id}-mode`}>
          Publication mode
        </label>
        <select id={`${id}-mode`} className={styles.select} value={fields.mode} onChange={(event) => change({ mode: event.target.value })}>
          <option value="review">review: you publish or discard each result</option>
          <option value="auto">auto: the service publishes</option>
        </select>
      </div>
      <div className={styles.fieldRow}>
        {(
          [
            ["hostCalls", "Host calls"],
            ["tokens", "Tokens"],
            ["seconds", "Seconds"],
          ] as const
        ).map(([key, label]) => (
          <div className={styles.field} key={key}>
            <label className={styles.label} htmlFor={`${id}-${key}`}>
              {label}
            </label>
            <input
              id={`${id}-${key}`}
              className={styles.input}
              type="number"
              inputMode="numeric"
              min={1}
              step={1}
              value={fields[key]}
              onChange={(event) => change({ [key]: event.target.value })}
            />
          </div>
        ))}
      </div>
      <div className={styles.checkRow}>
        <input
          id={`${id}-readonly`}
          className={styles.check}
          type="checkbox"
          checked={fields.readOnly}
          onChange={(event) => change({ readOnly: event.target.checked })}
        />
        <label htmlFor={`${id}-readonly`}>Read-only session</label>
      </div>
      {problem !== null ? (
        <Notice label="New session" tone="warning">
          <p className={styles.noticeText}>{problem}</p>
        </Notice>
      ) : null}
      <ActionNotice kinds={["create"]} label="New session request" />
      <button type="submit" className={`${styles.button} ${styles.primary}`} aria-disabled={sending} onClick={(event) => sending && event.preventDefault()}>
        {sending ? "Creating…" : "Create session"}
      </button>
    </form>
  );
}

export function LiveSessions() {
  const live = useLive();
  if (live === null) return null;
  const { model, controller } = live;
  const sessions = model.sessions;
  const unique = (pick: (session: NonNullable<typeof sessions>[number]) => string) => [...new Set((sessions ?? []).map(pick))];
  return (
    <>
      {model.authLost ? (
        <Notice label="Pairing ended" tone="warning">
          <p className={styles.noticeText}>
            The gateway no longer knows this browser. Restart tbound-webchat and open the new pairing URL it prints.
          </p>
        </Notice>
      ) : null}
      {model.fault !== null ? (
        <Notice label="Gateway read failed" tone="warning">
          <p className={styles.noticeText}>{model.fault}</p>
        </Notice>
      ) : null}
      {sessions === null ? (
        <p className={styles.hint}>Reading sessions…</p>
      ) : sessions.length === 0 ? (
        <p className={styles.hint}>No sessions yet. Create one below.</p>
      ) : (
        <ul aria-label="Sessions" className={styles.sessions}>
          {sessions.map((session) => (
            <li key={session.id}>
              <button
                type="button"
                className={styles.session}
                aria-current={session.id === model.activeId ? "true" : undefined}
                onClick={() => controller.select(session.id)}
              >
                <span className={styles.sessionTitle}>{`${session.project_id} · ${session.provider_profile}`}</span>
                <span className={styles.sessionMeta}>{session.id}</span>
                <span className={styles.chips}>
                  <Chip tone="idle">{`mode ${session.publication_mode}`}</Chip>
                  {session.read_only === true ? <Chip tone="idle">read-only</Chip> : null}
                  {session.blocked === true ? <Chip tone="deny">blocked</Chip> : null}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      <Disclosure summary="New session" defaultOpen={sessions !== null && sessions.length === 0}>
        <CreateForm projects={unique((session) => session.project_id)} providers={unique((session) => session.provider_profile)} />
      </Disclosure>
    </>
  );
}
