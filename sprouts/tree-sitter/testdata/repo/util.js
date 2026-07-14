import { readFile } from 'node:fs/promises';

/** An in-memory cache of parsed values. */
export class Cache {
  get(key) {
    return this.store[key];
  }
}

export function slugify(text) {
  return text.trim().toLowerCase();
}

export const identity = (value) => value;

// Two bindings in one statement: the head-widening must not fold both arrows
// into a single stub.
export const first = () => 1,
  second = () => 2;
