#import "Repo.h"
@implementation Repo
- (void)save:(int)x {}
+ (Repo *)create { return [[Repo alloc] init]; }
@end
