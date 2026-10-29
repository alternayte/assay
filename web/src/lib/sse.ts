/** One server-sent event. */
export type SSEvent = { event: string; data: string };

/** Reads server-sent events from a response body and calls onEvent for each. */
export async function readEvents(body: ReadableStream<Uint8Array>, onEvent: (e: SSEvent) => void) {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let event = "message";
  let data: string[] = [];
  const line = (l: string) => {
    if (l === "") {
      if (data.length > 0) onEvent({ event, data: data.join("\n") });
      event = "message";
      data = [];
    } else if (l.startsWith("event:")) {
      event = l.slice(6).trim();
    } else if (l.startsWith("data:")) {
      data.push(l.slice(5).replace(/^ /, ""));
    }
  };
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let i = buffer.indexOf("\n");
    while (i >= 0) {
      line(buffer.slice(0, i).replace(/\r$/, ""));
      buffer = buffer.slice(i + 1);
      i = buffer.indexOf("\n");
    }
  }
  if (buffer) line(buffer);
  line("");
}
