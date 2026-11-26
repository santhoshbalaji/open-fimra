import { HttpErrorResponse } from '@angular/common/http';

export function nationalWriteError(error: unknown): string {
  switch (error instanceof HttpErrorResponse ? error.status : 0) {
    case 400: return 'Check the form and try again.';
    case 403: return 'You do not have access';
    case 409: return 'This change conflicts with an earlier record. Check the dates or revision.';
    case 413: return 'Choose a signed JSON file under 8 MB.';
    case 422: return 'The signed extract failed verification. Check the state signing authority and file.';
    default: return 'Could not save the national record.';
  }
}
