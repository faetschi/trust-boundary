import {
  createExtensionRuntime,
  type ResourceLoader,
} from "@earendil-works/pi-coding-agent";

const emptyArray = Object.freeze([]) as unknown as any[];

/** ResourceLoader with no filesystem/project/user discovery and no reload path. */
export class LockedResourceLoader implements ResourceLoader {
  private readonly extensions = Object.freeze({
    extensions: emptyArray,
    errors: emptyArray,
    runtime: createExtensionRuntime(),
  });

  getExtensions() {
    return this.extensions;
  }

  getSkills() {
    return { skills: emptyArray, diagnostics: emptyArray };
  }

  getPrompts() {
    return { prompts: emptyArray, diagnostics: emptyArray };
  }

  getThemes() {
    return { themes: emptyArray, diagnostics: emptyArray };
  }

  getAgentsFiles() {
    return { agentsFiles: emptyArray };
  }

  getSystemPrompt() {
    return undefined;
  }

  getSystemPromptSource() {
    return undefined;
  }

  getAppendSystemPrompt() {
    return emptyArray;
  }

  getAppendSystemPromptSources() {
    return emptyArray;
  }

  extendResources(_paths: unknown): never {
    throw new Error("Resource extension is disabled in the closed profile");
  }

  async reload(): Promise<never> {
    throw new Error("Resource reload is disabled in the closed profile");
  }
}
