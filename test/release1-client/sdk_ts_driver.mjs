// Agent SDK driver for test/release1-client/sdk_ts.sh.
// Usage: node sdk_ts_driver.mjs <sdk.mjs> <claude binary> <settings file>
// Opens one session with the options T3 Code sets (streaming input, a preset
// system prompt with an append, partial messages, an explicit permission mode,
// the effort T3 sends by default for Opus 5.5, pathToClaudeCodeExecutable, a
// settings file), sends one prompt, and closes the input 5 seconds after the
// result, so the session-title request that the prompt starts can finish.
import { pathToFileURL } from "node:url";

const [sdkPath, claudeBin, settings] = process.argv.slice(2);
const { query } = await import(pathToFileURL(sdkPath).href);
let close;
const closed = new Promise((resolve) => {
  close = resolve;
});

async function* prompt() {
  yield {
    type: "user",
    message: { role: "user", content: "Reply with the single word ok." },
    parent_tool_use_id: null,
  };
  await closed;
}

for await (const message of query({
  prompt: prompt(),
  options: {
    pathToClaudeCodeExecutable: claudeBin,
    settings,
    permissionMode: "default",
    includePartialMessages: true,
    effort: "medium",
    systemPrompt: { type: "preset", preset: "claude_code", append: "Fixture host instructions." },
  },
})) {
  if (message.type === "result") {
    console.log(`result ${message.subtype}`);
    setTimeout(close, 5000);
  }
}
