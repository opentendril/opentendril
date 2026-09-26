// Incremental Server-Sent Events reader. Frames are assembled across arbitrary
// byte-chunk boundaries. Unknown fields are ignored. A frame is dispatched on
// a blank line, and a trailing frame is flushed when the stream ends.

export interface SSEFrame {
  event: string;
  data: string;
}

interface SSEAssembly {
  event: string;
  data: string[];
}

function emptyAssembly(): SSEAssembly {
  return { event: "", data: [] };
}

function consumeLine(line: string, assembly: SSEAssembly, onFrame: (frame: SSEFrame) => void) {
  const normalized = line.endsWith("\r") ? line.slice(0, -1) : line;
  if (normalized === "") {
    dispatchFrame(assembly, onFrame);
    return;
  }
  if (normalized.startsWith(":")) return;

  let field = normalized;
  let value = "";
  const colon = normalized.indexOf(":");
  if (colon >= 0) {
    field = normalized.slice(0, colon);
    value = normalized.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
  }

  if (field === "event") assembly.event = value;
  else if (field === "data") assembly.data.push(value);
}

function dispatchFrame(assembly: SSEAssembly, onFrame: (frame: SSEFrame) => void) {
  const hadContent = assembly.data.length > 0 || assembly.event !== "";
  const frame: SSEFrame = {
    event: assembly.event,
    data: assembly.data.join("\n"),
  };
  assembly.event = "";
  assembly.data = [];
  if (hadContent) onFrame(frame);
}

function consumeCompleteLines(
  buffer: string,
  assembly: SSEAssembly,
  onFrame: (frame: SSEFrame) => void,
): string {
  let rest = buffer;
  let newline = rest.indexOf("\n");
  while (newline >= 0) {
    consumeLine(rest.slice(0, newline), assembly, onFrame);
    rest = rest.slice(newline + 1);
    newline = rest.indexOf("\n");
  }
  return rest;
}

export async function readSSEFrames(
  stream: ReadableStream<Uint8Array>,
  onFrame: (frame: SSEFrame) => void,
  signal?: AbortSignal,
): Promise<void> {
  if (signal?.aborted) {
    throw new DOMException("The operation was aborted.", "AbortError");
  }

  const reader = stream.getReader();
  const decoder = new TextDecoder("utf-8");
  const assembly = emptyAssembly();
  let buffer = "";

  const abortReading = () => {
    void reader.cancel().catch(() => undefined);
  };
  signal?.addEventListener("abort", abortReading);

  try {
    for (;;) {
      if (signal?.aborted) {
        throw new DOMException("The operation was aborted.", "AbortError");
      }
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      buffer = consumeCompleteLines(buffer, assembly, onFrame);
    }
    buffer += decoder.decode();
    if (buffer.length > 0) {
      consumeLine(buffer, assembly, onFrame);
    }
    if (assembly.data.length > 0 || assembly.event !== "") {
      dispatchFrame(assembly, onFrame);
    }
  } catch (err) {
    await reader.cancel().catch(() => undefined);
    throw err;
  } finally {
    signal?.removeEventListener("abort", abortReading);
    try {
      reader.releaseLock();
    } catch {
      // cancel() already released the reader lock.
    }
  }
}
