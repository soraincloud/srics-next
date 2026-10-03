import test from 'node:test';
import assert from 'node:assert/strict';
import { hasChinesePunctuation, passwordBinding } from '../src/passwordInput.ts';

function editor(initial = '') {
  let text = initial;
  const updates: string[] = [], writes: string[] = [];
  const binding = passwordBinding(() => text, value => { writes.push(value); text = value; }, value => updates.push(value), initial);
  return { binding, updates, writes, type(value: string) { text = value; }, value: () => text };
}

test('periods, spaces, quotes and Unicode are submitted byte for byte', () => {
  for (const password of ['.r.u..123456.', ' ru.123456 ', 'a。b．c.', '"cafe\u0301"-🔑']) {
    const e = editor();
    for (let i = 1; i <= password.length; i++) { e.type(password.slice(0, i)); e.binding.sync(); }
    assert.equal(e.binding.readForSubmit(), password);
    assert.deepEqual(e.writes, []);
  }
});

test('a model echo or an unrelated render cannot overwrite newer native input', () => {
  const e = editor();
  e.type('r'); e.binding.sync();
  e.type('r.');
  e.binding.reconcile('r');
  assert.equal(e.value(), 'r.');
  assert.equal(e.binding.readForSubmit(), 'r.');
  assert.deepEqual(e.updates, ['r', 'r.']);
  assert.deepEqual(e.writes, []);
});

test('marked Pinyin text survives model echoes and cannot submit before completion', () => {
  const e = editor();
  e.binding.composition(true);
  e.type('ru'); e.binding.sync(); e.binding.reconcile('ru');
  assert.equal(e.binding.readForSubmit(), undefined);
  assert.equal(e.binding.blocksEnter({ key: 'Enter', keyCode: 13, isComposing: false }), true);
  e.type('如.'); e.binding.composition(false);
  assert.equal(e.binding.readForSubmit(), '如.');
  assert.equal(e.binding.blocksEnter({ key: 'Enter', keyCode: 229, isComposing: false }), true);
  assert.equal(e.binding.blocksEnter({ key: '.', keyCode: 190, isComposing: false }), false);
  assert.equal(e.binding.blocksEnter({ key: 'Enter', keyCode: 13, isComposing: false }), false);
  assert.deepEqual(e.writes, []);
});

test('submit and change synchronization recover AutoFill or missing input events', () => {
  const e = editor();
  e.type('autofill.r.u.123456');
  assert.equal(e.binding.readForSubmit(), 'autofill.r.u.123456');
  e.type('replacement.r.u.123456'); e.binding.sync();
  assert.deepEqual(e.updates, ['autofill.r.u.123456', 'replacement.r.u.123456']);
});

test('explicit clearing removes marked input and allows a fresh password', () => {
  const e = editor();
  e.binding.composition(true); e.type('ru'); e.binding.sync();
  e.binding.reconcile('');
  assert.equal(e.value(), '');
  assert.equal(e.binding.readForSubmit(), '');
  e.type('new.123456');
  assert.equal(e.binding.readForSubmit(), 'new.123456');
  assert.deepEqual(e.writes, ['']);
});

test('Chinese punctuation is identified without rewriting a valid password', () => {
  assert.equal(hasChinesePunctuation('ordinary.r.u.123456'), false);
  for (const value of ['r。u', 'r．u', '“quoted”', '密码！']) assert.equal(hasChinesePunctuation(value), true);
});
