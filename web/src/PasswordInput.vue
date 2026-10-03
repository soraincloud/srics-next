<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, useAttrs, watch } from "vue";
import Icon from "./Icon.vue";
import { hasChinesePunctuation, passwordBinding } from "./passwordInput";

defineOptions({ inheritAttrs: false });
const props = withDefaults(defineProps<{
  id: string;
  label: string;
  modelValue: string;
  autocomplete?: string;
  disabled?: boolean;
}>(), { autocomplete: "current-password", disabled: false });
const emit = defineEmits<{ "update:modelValue": [value: string] }>();
const attrs = useAttrs();
const input = ref<HTMLInputElement>(), revealed = ref(false);
const binding = passwordBinding(
  () => input.value?.value ?? props.modelValue,
  value => { if (input.value) input.value.value = value; },
  value => emit("update:modelValue", value),
  props.modelValue,
);
const punctuationHint = computed(() => {
  if (props.modelValue.includes("。")) return "输入中有中文句号「。」，与英文「.」不同。输入英文符号请切换 ABC 输入法。";
  if (props.modelValue.includes("．")) return "输入中有全角句点「．」，与英文「.」不同。输入英文符号请切换 ABC 输入法。";
  return hasChinesePunctuation(props.modelValue) ? "含中文或全角标点，请确认与设置时一致。输入英文符号可切换 ABC 输入法。" : "";
});
function description() { return [attrs["aria-describedby"], punctuationHint.value ? props.id + "-input-hint" : ""].filter(Boolean).join(" ") || undefined; }
function hide() { revealed.value = false; }
function keydown(event: KeyboardEvent) {
  if (binding.blocksEnter(event)) { event.preventDefault(); event.stopPropagation(); }
}
async function toggle() {
  const field = input.value;
  if (!field || props.disabled) return;
  binding.sync();
  const start = field.selectionStart, end = field.selectionEnd;
  revealed.value = !revealed.value;
  await nextTick();
  field.focus({ preventScroll: true });
  if (start !== null && end !== null) field.setSelectionRange(start, end);
}
watch(() => props.modelValue, value => {
  if (binding.reconcile(value) && !value) hide();
}, { flush: "sync" });
onMounted(() => {
  // Preserve a password already inserted by the browser's AutoFill.
  if (input.value && !input.value.value) input.value.value = props.modelValue;
  binding.sync();
  window.addEventListener("blur", hide);
});
onUnmounted(() => {
  window.removeEventListener("blur", hide);
  if (input.value) input.value.value = "";
  binding.forget();
});
defineExpose({ read: binding.readForSubmit, hide });
</script>

<template>
  <div class="password-field">
    <div class="password-input">
      <input
        v-bind="attrs"
        :id="id"
        ref="input"
        :type="revealed ? 'text' : 'password'"
        :autocomplete="autocomplete"
        :autocorrect.attr="'off'"
        autocapitalize="none"
        :spellcheck="false"
        :disabled="disabled"
        :aria-describedby="description()"
        @input="binding.sync()"
        @change="binding.sync()"
        @blur="binding.sync()"
        @keyup="binding.sync()"
        @keydown="keydown"
        @compositionstart="binding.composition(true)"
        @compositionend="binding.composition(false)"
      />
      <button type="button" class="password-toggle" :disabled="disabled"
        :aria-label="(revealed ? '隐藏' : '显示') + label"
        :title="(revealed ? '隐藏' : '显示') + label" :aria-pressed="revealed"
        @mousedown.prevent @click="toggle">
        <Icon :name="revealed ? 'eye-off' : 'eye'" />
      </button>
    </div>
    <p v-if="punctuationHint" :id="id + '-input-hint'" class="password-hint password-input-hint" role="status">{{ punctuationHint }}</p>
  </div>
</template>
