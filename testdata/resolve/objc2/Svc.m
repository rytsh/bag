#import "Base.h"
@interface Svc : Base {
  Repo *_ivar;
}
@end
@implementation Svc
- (void)run {
  Repo *r = [Repo shared];
  [r save:1];
  [Repo shared];
  [self helper];
  [self.repo save:2];
  [_ivar save:3];
  [Store put];
  [unknown thing];
}
- (void)helper {}
@end
