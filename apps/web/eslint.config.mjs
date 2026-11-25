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
    files: ['**/*.html'],
    extends: [...angular.configs.templateRecommended, ...angular.configs.templateAccessibility]
  }
);
