import { describe, it, expect } from 'vitest';
import * as types from '../src/index.js';

describe('shared-types module', () => {
  it('loads without error', () => {
    expect(types).toBeDefined();
  });
});
