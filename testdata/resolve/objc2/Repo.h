@interface Repo : NSObject
- (void)save:(int)x;
+ (Repo *)shared;
@end
@protocol Store
- (void)put;
@end
