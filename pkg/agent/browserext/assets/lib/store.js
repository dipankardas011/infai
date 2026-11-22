// chrome.storage.session helpers. All read-modify-write updates go through one
// promise chain so concurrent handlers cannot clobber each other's writes.

let chain = Promise.resolve();

export async function read(key, fallback = {}) {
  const got = await chrome.storage.session.get(key);
  return got[key] ?? fallback;
}

// fn receives the current value and returns the new one, or undefined to skip
// the write.
export function mutate(key, fn, fallback = {}) {
  const run = chain.then(async () => {
    const next = fn(await read(key, fallback));
    if (next !== undefined) await chrome.storage.session.set({ [key]: next });
  });
  chain = run.catch(() => {});
  return run;
}

export function write(key, value) {
  return chrome.storage.session.set({ [key]: value });
}
