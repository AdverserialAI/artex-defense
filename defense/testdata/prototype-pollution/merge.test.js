'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const merge = require('./merge.js');

test('existing nested merge behavior', () => {
  assert.deepEqual(merge({ nested: { left: 1 } }, { nested: { right: 2 } }), {
    nested: { left: 1, right: 2 },
  });
});
