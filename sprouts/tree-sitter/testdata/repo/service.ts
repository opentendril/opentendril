import { Cache } from './util.js';

/** A user record. */
export interface User {
  id: number;
  name: string;
}

export type UserId = number;

@Injectable()
export class UserService {
  /** Look a user up by id. */
  @traced
  async find(id: UserId): Promise<User | null> {
    return new Cache().get(String(id));
  }
}

export enum Role {
  Admin,
  Guest,
}
