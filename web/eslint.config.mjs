import js from '@eslint/js';
import pluginVue from 'eslint-plugin-vue';
import { withVueTs, vueTsConfigs } from '@vue/eslint-config-typescript';
import eslintConfigPrettier from 'eslint-config-prettier/flat';
import { configs as importXConfigs } from 'eslint-plugin-import-x';
import pluginPromise from 'eslint-plugin-promise';
import pluginVueA11y from 'eslint-plugin-vuejs-accessibility';
import globals from 'globals';

export default withVueTs(
  {
    rootDir: import.meta.dirname,
    scriptLangs: ['ts'],
    tsSyntaxInTemplates: true,
  },

  {
    ignores: ['node_modules/**', 'dist/**', 'coverage/**'],
  },

  js.configs.recommended,

  // Vue 官方完整推荐规则，全部提升为 error（含 strongly-recommended）
  pluginVue.configs['flat/recommended-error'],

  // 无障碍规则
  pluginVueA11y.configs['flat/recommended'],

  // 模块导入规范
  importXConfigs['flat/recommended'],

  // Promise 使用规范
  pluginPromise.configs['flat/recommended'],

  // TypeScript 严格类型规则
  vueTsConfigs.strictTypeChecked,

  // TypeScript 严格风格规则
  vueTsConfigs.stylisticTypeChecked,

  {
    languageOptions: {
      globals: {
        ...globals.browser,
      },
    },
    rules: {
      // TypeScript 类型安全
      '@typescript-eslint/no-explicit-any': 'error',
      '@typescript-eslint/no-non-null-assertion': 'error',
      '@typescript-eslint/consistent-type-imports': 'error',

      // Promise 安全
      '@typescript-eslint/no-floating-promises': 'error',
      '@typescript-eslint/no-misused-promises': 'error',
      '@typescript-eslint/await-thenable': 'error',
      '@typescript-eslint/no-unnecessary-type-assertion': 'error',

      // 禁止绕过类型检查
      '@typescript-eslint/ban-ts-comment': [
        'error',
        {
          'ts-ignore': true,
          'ts-nocheck': true,
          'ts-expect-error': 'allow-with-description',
          minimumDescriptionLength: 10,
        },
      ],

      // JavaScript 基础规范
      'no-console': 'error',
      'no-debugger': 'error',
      'no-alert': 'error',
      eqeqeq: ['error', 'always'],
      curly: ['error', 'all'],
      'prefer-const': 'error',
      'no-duplicate-imports': 'error',

      // Promise 插件默认以 warn 表达的规则统一提升为 error
      'promise/no-nesting': 'error',
      'promise/no-promise-in-callback': 'error',
      'promise/no-callback-in-promise': 'error',
      'promise/no-return-in-finally': 'error',
      'promise/valid-params': 'error',

      // 模板字符串里允许直接写数字（例如 `${ms} ms`）
      '@typescript-eslint/restrict-template-expressions': ['error', { allowNumber: true }],

      // 保留字符串「空串也算缺省」的语义
      '@typescript-eslint/prefer-nullish-coalescing': ['error', { ignorePrimitives: true }],

      // 模块解析交给 TypeScript：import-x 自带的 node 解析器不识别 .ts / .vue 扩展名
      'import-x/no-unresolved': 'off',

      // label 关联控件：允许「嵌套控件」或「for + id」二选一
      'vuejs-accessibility/label-has-for': ['error', { required: { some: ['nesting', 'id'] } }],
    },
  },

  // 关闭与 Prettier 冲突的规则，必须放最后
  eslintConfigPrettier,
);
