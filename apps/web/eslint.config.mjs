import { defineConfig } from 'eslint/config';
import angular from 'angular-eslint';
import ts from 'typescript-eslint';

export default defineConfig(
  {
    files: ['**/*.ts'],
    extends: [...ts.configs.recommended, ...angular.configs.tsRecommended],
    processor: angular.processInlineTemplates
  },
  {
    // Generated Helm controls expose the standard class, style and checked bindings.
    files: ['src/ui/*/src/**/*.ts'],
    rules: { '@angular-eslint/no-input-rename': 'off' }
  },
  {
    files: ['**/*.html'],
    extends: [...angular.configs.templateRecommended, ...angular.configs.templateAccessibility],
    rules: {
      '@angular-eslint/template/label-has-associated-control': ['error', {
        controlComponents: ['hlm-checkbox', 'hlm-radio', 'hlm-switch']
      }]
    }
  }
);
