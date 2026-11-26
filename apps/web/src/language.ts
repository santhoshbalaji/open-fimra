import { signal } from '@angular/core';

type Catalog = Record<string, string>;
const active = signal({ language: 'en', messages: {} as Catalog });
const catalogs = new Map<string, Catalog>();
let request = 0;

export const language = () => active().language;
export const formatLocale = (code = language()) => code === 'en' ? 'en-IN' : code === 'hi' ? 'hi-IN' : code;

export function translate(message: string, values: readonly unknown[] = []): string {
  const messages = active().messages;
  const text = Object.hasOwn(messages, message) ? messages[message] : message;
  return values.length ? text.replace(/\{(\d+)\}/g, (_, index) => String(values[+index] ?? '')) : text;
}

export function matchLanguage(candidate: string, supported: readonly string[]): string | undefined {
  const code = candidate.toLowerCase();
  return supported.find(tag => tag.toLowerCase() === code) ??
    supported.find(tag => tag.toLowerCase() === code.split('-')[0]);
}

export function preferredLanguage(supported: readonly string[], workspace?: string): string {
  for (const candidate of [workspace, ...(navigator.languages ?? [navigator.language])]) {
    const match = candidate && matchLanguage(candidate, supported);
    if (match) return match;
  }
  return 'en';
}

export function languageName(code: string): string {
  try {
    return new Intl.DisplayNames([code], { type: 'language' }).of(code) ?? code;
  } catch {
    return code;
  }
}

export async function activateLanguage(code: string): Promise<void> {
  const id = ++request;
  let messages = catalogs.get(code);
  if (!messages && code !== 'en') {
    try {
      const response = await fetch(new URL('locales/' + encodeURIComponent(code) + '.json', document.baseURI));
      if (response.ok) {
        messages = await response.json() as Catalog;
        catalogs.set(code, messages);
      }
    } catch {
      // Keep the current language if an optional catalog cannot be downloaded.
    }
    if (!messages) return;
  }
  if (id === request) {
    document.documentElement.lang = code;
    active.set({ language: code, messages: messages ?? {} });
  }
}
